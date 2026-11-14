package service

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/media"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/storage"
)

func newTestService(t *testing.T) (*Service, []byte, []byte) {
	t.Helper()
	repository, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatalf("repository: %v", err)
	}
	audio, video, err := media.CalibrationPair()
	if err != nil {
		t.Fatalf("calibration pair: %v", err)
	}
	return New(repository), audio, video
}

func admitInput(id string, audio, video []byte) domain.CreateTakeInput {
	return domain.CreateTakeInput{
		TakeID:      id,
		Name:        "service take",
		AudioBase64: base64.StdEncoding.EncodeToString(audio),
		VideoBase64: base64.StdEncoding.EncodeToString(video),
	}
}

func TestCreateTakeValidationAndProbeFailures(t *testing.T) {
	svc, audio, video := newTestService(t)

	t.Run("strict base64 rejected", func(t *testing.T) {
		input := admitInput("take-bad-b64", audio, video)
		input.AudioBase64 = "not valid base64!"
		if _, err := svc.CreateTake(input); !domain.IsErrorCode(err, "invalid_base64") {
			t.Fatalf("got %v, want invalid_base64", err)
		}
	})
	t.Run("empty payload rejected", func(t *testing.T) {
		input := admitInput("take-empty", audio, video)
		input.VideoBase64 = ""
		if _, err := svc.CreateTake(input); !domain.IsErrorCode(err, "invalid_request") {
			t.Fatalf("got %v, want invalid_request", err)
		}
	})
	t.Run("oversized payload rejected", func(t *testing.T) {
		input := admitInput("take-huge", audio, video)
		input.AudioBase64 = base64.StdEncoding.EncodeToString(make([]byte, domain.MaxAudioBytes+1))
		if _, err := svc.CreateTake(input); !domain.IsErrorCode(err, "payload_too_large") {
			t.Fatalf("got %v, want payload_too_large", err)
		}
	})
	t.Run("invalid audio probed", func(t *testing.T) {
		input := admitInput("take-bad-audio", []byte("not wav"), video)
		if _, err := svc.CreateTake(input); !domain.IsErrorCode(err, "invalid_audio") {
			t.Fatalf("got %v, want invalid_audio", err)
		}
	})
	t.Run("invalid video probed", func(t *testing.T) {
		input := admitInput("take-bad-video", audio, []byte("not y4m"))
		if _, err := svc.CreateTake(input); !domain.IsErrorCode(err, "invalid_video") {
			t.Fatalf("got %v, want invalid_video", err)
		}
	})

	// Every failure must leave no visible take behind.
	for _, id := range []string{"take-bad-b64", "take-empty", "take-huge", "take-bad-audio", "take-bad-video"} {
		if _, err := svc.GetTake(id); !domain.IsErrorCode(err, "take_not_found") {
			t.Fatalf("failed admission for %s left a visible take: %v", id, err)
		}
	}
}

func TestCreateTakeIdempotentAndConflict(t *testing.T) {
	svc, audio, video := newTestService(t)
	input := admitInput("take-idem", audio, video)

	first, err := svc.CreateTake(input)
	if err != nil || !first.Created {
		t.Fatalf("first create: created=%v err=%v", first.Created, err)
	}
	second, err := svc.CreateTake(input)
	if err != nil || second.Created {
		t.Fatalf("identical retry must be idempotent: created=%v err=%v", second.Created, err)
	}
	if first.Take.Digest != second.Take.Digest {
		t.Fatal("idempotent retry returned a different digest")
	}

	conflict := input
	conflict.Name = "a different name entirely"
	if _, err := svc.CreateTake(conflict); !domain.IsErrorCode(err, "take_conflict") {
		t.Fatalf("got %v, want take_conflict", err)
	}
}

func TestAnalyzeOffsetPolicyFailures(t *testing.T) {
	svc, audio, video := newTestService(t)
	if _, err := svc.CreateTake(admitInput("take-policy", audio, video)); err != nil {
		t.Fatalf("admit: %v", err)
	}

	// A ceiling below the measured 125 ms offset produces a terminal failed
	// analysis carrying the stable offset_exceeded code and no usable events.
	result, err := svc.Analyze("take-policy", domain.CreateAnalysisInput{
		AnalysisID: "ana-offset", MinConfidence: 0.0, MaxOffsetMS: 10,
	})
	if err != nil {
		t.Fatalf("analyze should persist a failed record, not return a transport error: %v", err)
	}
	if result.Analysis.State != domain.AnalysisStateFailed ||
		result.Analysis.ErrorCode != "offset_exceeded" {
		t.Fatalf("expected offset_exceeded failure, got %+v", result.Analysis)
	}
	if result.Analysis.OffsetMS != nil || result.Analysis.AudioEventMS != nil {
		t.Fatal("failed analysis must not expose a usable event pair")
	}
	// The failed record is readable from the repository and is terminal.
	stored, err := svc.GetAnalysis("ana-offset")
	if err != nil || stored.State != domain.AnalysisStateFailed {
		t.Fatalf("stored failed analysis wrong: %+v err=%v", stored, err)
	}
}

func TestConformRequiresReadyAnalysis(t *testing.T) {
	svc, audio, video := newTestService(t)
	if _, err := svc.CreateTake(admitInput("take-ready", audio, video)); err != nil {
		t.Fatalf("admit: %v", err)
	}
	if _, err := svc.Analyze("take-ready", domain.CreateAnalysisInput{
		AnalysisID: "ana-failed", MinConfidence: 0.0, MaxOffsetMS: 10,
	}); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	// A conform against a failed analysis is refused with analysis_not_ready.
	if _, err := svc.CreateConform("ana-failed", domain.CreateConformInput{
		ConformID: "cfm-blocked", MaxCorrectionMS: 500,
	}); !domain.IsErrorCode(err, "analysis_not_ready") {
		t.Fatalf("got %v, want analysis_not_ready", err)
	}

	// An unknown analysis id is reported through the same boundary.
	if _, err := svc.CreateConform("ana-missing", domain.CreateConformInput{
		ConformID: "cfm-missing", MaxCorrectionMS: 500,
	}); !domain.IsErrorCode(err, "analysis_not_found") {
		t.Fatalf("got %v, want analysis_not_found", err)
	}
}

func TestSuccessfulWorkflowRecordConsistency(t *testing.T) {
	svc, audio, video := newTestService(t)
	if _, err := svc.CreateTake(admitInput("take-happy", audio, video)); err != nil {
		t.Fatalf("admit: %v", err)
	}
	analysis, err := svc.Analyze("take-happy", domain.CreateAnalysisInput{
		AnalysisID: "ana-happy", MinConfidence: 0.55, MaxOffsetMS: 1000,
	})
	if err != nil || analysis.Analysis.State != domain.AnalysisStateReady {
		t.Fatalf("analysis: %+v err=%v", analysis.Analysis, err)
	}
	conform, err := svc.CreateConform("ana-happy", domain.CreateConformInput{
		ConformID: "cfm-happy", MaxCorrectionMS: 500,
	})
	if err != nil || conform.Conform.State != domain.ConformStateCompleted {
		t.Fatalf("conform: %+v err=%v", conform.Conform, err)
	}
	bundle, receipt, err := svc.ReadConformBundle("cfm-happy")
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if len(bundle) != conform.Conform.BundleBytes {
		t.Fatal("bundle byte count disagrees with the completed record")
	}
	if !strings.HasPrefix(receipt.ConformID, "cfm-happy") {
		t.Fatalf("receipt identity wrong: %s", receipt.ConformID)
	}
}
