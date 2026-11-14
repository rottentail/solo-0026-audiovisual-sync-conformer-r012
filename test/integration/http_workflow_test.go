package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/test/synctest"
)

// rateMatrix covers the production-accepted sample rates, both channel
// layouts, integer and fractional picture frame rates, and both signs of the
// measured offset. Every case drives the full HTTP workflow with a fixture
// whose ground-truth offset is known from its construction parameters.
func rateMatrix() []struct {
	name        string
	spec        synctest.FixtureSpec
	toleranceMS int
} {
	base := synctest.DefaultSpec()
	integer := func(mutate func(*synctest.FixtureSpec)) synctest.FixtureSpec {
		spec := base
		mutate(&spec)
		return spec
	}
	return []struct {
		name        string
		spec        synctest.FixtureSpec
		toleranceMS int
	}{
		{
			name:        "48k mono 24fps positive offset",
			spec:        base,
			toleranceMS: 1,
		},
		{
			name: "44.1k mono 24fps positive offset",
			spec: integer(func(s *synctest.FixtureSpec) {
				s.SampleRate = 44100
				s.AudioEventMS = 480
			}),
			toleranceMS: 1,
		},
		{
			name: "96k stereo 30fps negative offset",
			spec: integer(func(s *synctest.FixtureSpec) {
				s.SampleRate = 96000
				s.Channels = 2
				s.FPSNum = 30
				s.FPSDen = 1
				s.FlashFrame = 15
				s.AudioEventMS = 250
			}),
			toleranceMS: 1,
		},
		{
			name: "48k mono NTSC 30000/1001 positive offset",
			spec: integer(func(s *synctest.FixtureSpec) {
				s.FPSNum = 30000
				s.FPSDen = 1001
				s.FlashFrame = 9
				s.AudioEventMS = 600
			}),
			toleranceMS: 34,
		},
		{
			name: "44.1k stereo NTSC 24000/1001 negative offset",
			spec: integer(func(s *synctest.FixtureSpec) {
				s.SampleRate = 44100
				s.Channels = 2
				s.FPSNum = 24000
				s.FPSDen = 1001
				s.FlashFrame = 15
				s.AudioEventMS = 250
			}),
			toleranceMS: 42,
		},
		{
			name: "48k mono NTSC 60000/1001 positive offset",
			spec: integer(func(s *synctest.FixtureSpec) {
				s.FPSNum = 60000
				s.FPSDen = 1001
				s.Frames = 40
				s.FlashFrame = 20
				s.AudioEventMS = 700
			}),
			toleranceMS: 17,
		},
		{
			name: "22.05k mono 25fps negative offset",
			spec: integer(func(s *synctest.FixtureSpec) {
				s.SampleRate = 22050
				s.FPSNum = 25
				s.FPSDen = 1
				s.FlashFrame = 12
				s.AudioEventMS = 200
			}),
			toleranceMS: 1,
		},
	}
}

func TestKnownOffsetAnalysisAndConformMatrix(t *testing.T) {
	for index, tc := range rateMatrix() {
		t.Run(tc.name, func(t *testing.T) {
			fixture, err := synctest.BuildFixture(tc.spec)
			if err != nil {
				t.Fatalf("build fixture: %v", err)
			}
			h := synctest.NewHarness(t)
			takeID := fmt.Sprintf("take-%02d", index)
			analysisID := fmt.Sprintf("analysis-%02d", index)
			conformID := fmt.Sprintf("conform-%02d", index)

			h.AdmitTake(takeID, "matrix take", fixture)
			status, analysis := h.CreateAnalysis(takeID, analysisID, 0.55, 5000)
			if status != http.StatusCreated {
				t.Fatalf("analysis status %d", status)
			}
			if analysis.State != domain.AnalysisStateReady {
				t.Fatalf("analysis state %s: %s", analysis.State, analysis.ErrorMessage)
			}
			assertAnalysisMatchesFixture(t, fixture, analysis, tc.toleranceMS)

			// A repeated analysis request is idempotent and returns the same
			// immutable terminal record over HTTP.
			retryStatus, retry := h.CreateAnalysis(takeID, analysisID, 0.55, 5000)
			if retryStatus != http.StatusOK || retry.State != domain.AnalysisStateReady {
				t.Fatalf("idempotent analysis retry returned %d/%s", retryStatus, retry.State)
			}

			ceiling := 5000
			status, conform := h.CreateConform(analysisID, conformID, ceiling)
			if status != http.StatusCreated || conform.State != domain.ConformStateCompleted {
				t.Fatalf("conform status %d state %s: %s", status, conform.State, conform.ErrorMessage)
			}
			status, bundle := h.GetBundle(conformID)
			if status != http.StatusOK {
				t.Fatalf("bundle status %d", status)
			}
			verification, err := synctest.VerifyBundle(
				bundle, conform, analysis, fixture.Audio, fixture.Video,
			)
			if err != nil {
				t.Fatalf("bundle/record consistency check failed: %v", err)
			}
			if abs(verification.ResidualMS) > tc.toleranceMS {
				t.Fatalf("aligned residual %d ms exceeds tolerance %d ms",
					verification.ResidualMS, tc.toleranceMS)
			}
			// The GET conform route must expose the same digest as the served ZIP.
			_, fetched := h.GetConform(conformID)
			if fetched.BundleDigest != verification.BundleDigest {
				t.Fatal("conform record digest disagrees with served bundle")
			}
		})
	}
}

func assertAnalysisMatchesFixture(
	t *testing.T,
	fixture synctest.Fixture,
	analysis domain.SyncAnalysis,
	toleranceMS int,
) {
	t.Helper()
	if analysis.AudioEventMS == nil || analysis.VideoEventMS == nil || analysis.OffsetMS == nil {
		t.Fatalf("ready analysis lacks event fields: %+v", analysis)
	}
	if delta := abs(*analysis.AudioEventMS - fixture.AudioEventMS); delta > 1 {
		t.Fatalf("audio event %d ms, ground truth %d ms", *analysis.AudioEventMS, fixture.AudioEventMS)
	}
	if delta := abs(*analysis.VideoEventMS - fixture.VideoEventMS); delta > toleranceMS {
		t.Fatalf("picture event %d ms, ground truth %d ms (tolerance %d)",
			*analysis.VideoEventMS, fixture.VideoEventMS, toleranceMS)
	}
	wantOffset := fixture.AudioEventMS - fixture.VideoEventMS
	if delta := abs(*analysis.OffsetMS - wantOffset); delta > toleranceMS {
		t.Fatalf("measured offset %d ms, known offset %d ms (tolerance %d)",
			*analysis.OffsetMS, wantOffset, toleranceMS)
	}
	if analysis.Confidence < 0.55 {
		t.Fatalf("confidence %.3f below policy floor", analysis.Confidence)
	}
}

func TestCorrectionCeilingAndFailedTerminal(t *testing.T) {
	fixture, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	h := synctest.NewHarness(t)
	h.AdmitTake("take-ceiling", "ceiling take", fixture)
	_, analysis := h.CreateAnalysis("take-ceiling", "ana-ceiling", 0.55, 5000)
	if analysis.State != domain.AnalysisStateReady || analysis.OffsetMS == nil {
		t.Fatalf("analysis not ready: %+v", analysis)
	}

	// A ceiling below the measured correction must produce a terminal failure.
	status, raw := h.Request(
		http.MethodPost,
		"/v1/sync-analyses/ana-ceiling/conforms",
		map[string]any{"conform_id": "cfm-too-big", "max_correction_ms": 50},
	)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("oversized correction status %d: %s", status, raw)
	}
	if code := synctest.ErrorCode(t, raw); code != "correction_limit_exceeded" {
		t.Fatalf("unexpected error code %q", code)
	}
	_, failed := h.GetConform("cfm-too-big")
	if failed.State != domain.ConformStateFailed || failed.ErrorCode != "correction_limit_exceeded" {
		t.Fatalf("unexpected failed conform record: %+v", failed)
	}
	// A failed job never exposes a bundle, even after re-reading.
	if status, _ := h.GetBundle("cfm-too-big"); status != http.StatusConflict {
		t.Fatalf("failed conform bundle route returned %d, want 409", status)
	}
	// The same conform id retried returns the existing failed record (no rerun).
	retryStatus, retry := h.CreateConform("ana-ceiling", "cfm-too-big", 50)
	if retryStatus != http.StatusOK || retry.State != domain.ConformStateFailed {
		t.Fatalf("failed conform retry returned %d/%s", retryStatus, retry.State)
	}
}

func TestFailedAnalysisHasNoUsableEventPair(t *testing.T) {
	// A picture stream with no flash forces sync_detection_failed.
	spec := synctest.DefaultSpec()
	fixture, err := synctest.BuildFixture(spec)
	if err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	// Rebuild video with uniform luma by replacing the bright frame bytes.
	noFlash := stripFlash(t, fixture.Video, spec)
	fixture.Video = noFlash

	h := synctest.NewHarness(t)
	h.AdmitTake("take-no-flash", "no flash", fixture)
	status, analysis := h.CreateAnalysis("take-no-flash", "ana-no-flash", 0.55, 5000)
	if status != http.StatusCreated {
		t.Fatalf("analysis status %d", status)
	}
	if analysis.State != domain.AnalysisStateFailed {
		t.Fatalf("expected failed analysis, got %s", analysis.State)
	}
	if analysis.ErrorCode == "" || analysis.OffsetMS != nil || analysis.AudioEventMS != nil {
		t.Fatalf("failed analysis leaked a usable event pair: %+v", analysis)
	}
	// A conform against a failed analysis is refused.
	status, raw := h.Request(
		http.MethodPost,
		"/v1/sync-analyses/ana-no-flash/conforms",
		map[string]any{"conform_id": "cfm-blocked", "max_correction_ms": 5000},
	)
	if status != http.StatusConflict {
		t.Fatalf("conform on failed analysis returned %d: %s", status, raw)
	}
	if code := synctest.ErrorCode(t, raw); code != "analysis_not_ready" {
		t.Fatalf("unexpected error code %q", code)
	}
	if status, _ := h.GetBundle("cfm-blocked"); status != http.StatusNotFound {
		t.Fatalf("blocked conform bundle route returned %d, want 404", status)
	}
}

func TestPolicyFailedAnalysisLeaksNoEventsOverHTTP(t *testing.T) {
	fixture, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("build fixture: %v", err)
	}
	h := synctest.NewHarness(t)
	h.AdmitTake("take-policy-http", "policy take", fixture)
	// A max offset below the measured offset forces an offset_exceeded failure.
	status, body := h.Request(
		http.MethodPost,
		"/v1/takes/take-policy-http/sync-analyses",
		map[string]any{"analysis_id": "ana-policy-http", "min_confidence": 0.0, "max_offset_ms": 10},
	)
	if status != http.StatusCreated {
		t.Fatalf("policy-failing analysis status %d: %s", status, body)
	}
	var failed domain.SyncAnalysis
	if err := json.Unmarshal(body, &failed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failed.State != domain.AnalysisStateFailed || failed.ErrorCode != "offset_exceeded" {
		t.Fatalf("expected offset_exceeded, got %+v", failed)
	}
	// The terminal failed record served over HTTP must carry no event pair.
	if failed.OffsetMS != nil || failed.AudioEventMS != nil || failed.VideoEventMS != nil {
		t.Fatalf("failed analysis leaked event data over HTTP: %+v", failed)
	}
	// Reading it back through the public route shows the same sanitized record.
	status, reread := h.GetAnalysis("ana-policy-http")
	if status != http.StatusOK || reread.State != domain.AnalysisStateFailed || reread.OffsetMS != nil {
		t.Fatalf("stored failed analysis leaks events: %d %+v", status, reread)
	}
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
