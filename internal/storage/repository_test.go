package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

func sampleTake(id string) domain.MediaTake {
	return domain.MediaTake{
		ID:         id,
		Name:       "fixture take",
		State:      domain.TakeStateAdmitted,
		Digest:     "digest-" + id,
		Audio:      domain.AudioInfo{Digest: "audio-" + id, SampleRate: 48000, Channels: 1},
		Video:      domain.VideoInfo{Digest: "video-" + id, FPSNum: 24, FPSDen: 1, FrameCount: 12},
		AdmittedAt: time.Now().UTC(),
	}
}

func TestAdmitTakeAtomicAndIdempotent(t *testing.T) {
	repo := newTestRepository(t)
	record := sampleTake("take-01")

	admitted, created, err := repo.AdmitTake(record, []byte("audio-bytes"), []byte("video-bytes"))
	if err != nil || !created {
		t.Fatalf("first admission: created=%v err=%v", created, err)
	}
	if admitted.ID != "take-01" {
		t.Fatalf("unexpected admitted record %q", admitted.ID)
	}

	// The complete namespace must be visible with all three files.
	dir := repo.takeDir("take-01")
	for _, name := range []string{"audio.wav", "picture.y4m", "record.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected published file %s: %v", name, err)
		}
	}
	// No staging directories may linger.
	if leftovers, _ := os.ReadDir(filepath.Join(repo.Root(), "tmp")); len(leftovers) != 0 {
		t.Fatalf("staging directory leaked: %v", leftovers)
	}

	// Identical retry returns the existing record without creating a new one.
	retry, created, err := repo.AdmitTake(record, []byte("audio-bytes"), []byte("video-bytes"))
	if err != nil || created {
		t.Fatalf("identical retry: created=%v err=%v", created, err)
	}
	if retry.Digest != record.Digest {
		t.Fatal("retry returned a different record")
	}

	// Conflicting bytes are rejected and leave the original untouched.
	conflict := record
	conflict.Audio.Digest = "different-audio"
	if _, _, err := repo.AdmitTake(conflict, []byte("other"), []byte("video-bytes")); err == nil {
		t.Fatal("conflicting admission must fail")
	} else if !domain.IsErrorCode(err, "take_conflict") {
		t.Fatalf("expected take_conflict, got %v", err)
	}
	audio, err := os.ReadFile(filepath.Join(dir, "audio.wav"))
	if err != nil || string(audio) != "audio-bytes" {
		t.Fatal("original take payload was modified by a conflicting retry")
	}
}

func TestFailedProbeLeavesNoTakeDirectory(t *testing.T) {
	repo := newTestRepository(t)
	missing, err := repo.GetTake("never-created")
	if err == nil || !domain.IsErrorCode(err, "take_not_found") {
		t.Fatalf("expected take_not_found, got record=%v err=%v", missing, err)
	}
	if _, err := os.Stat(repo.takeDir("never-created")); !os.IsNotExist(err) {
		t.Fatalf("no take directory should exist, stat err=%v", err)
	}
}

func TestAnalysisLifecycleTransitions(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Now().UTC()
	record := domain.SyncAnalysis{
		ID: "ana-01", TakeID: "take-01", State: domain.AnalysisStateQueued,
		Policy:    domain.SyncPolicy{MinConfidence: 0.55, MaxOffsetMS: 1000},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, created, err := repo.CreateAnalysis(record); err != nil || !created {
		t.Fatalf("create analysis: created=%v err=%v", created, err)
	}
	// Duplicate create with the same policy is idempotent.
	if _, created, err := repo.CreateAnalysis(record); err != nil || created {
		t.Fatalf("duplicate analysis: created=%v err=%v", created, err)
	}
	// Skipping the detecting state must be rejected.
	illegal := record
	illegal.State = domain.AnalysisStateReady
	if err := repo.UpdateAnalysis(illegal); err == nil {
		t.Fatal("queued -> ready transition must be rejected")
	}

	record.State = domain.AnalysisStateDetecting
	if err := repo.UpdateAnalysis(record); err != nil {
		t.Fatalf("queued -> detecting: %v", err)
	}
	offset := 125
	record.State = domain.AnalysisStateReady
	record.OffsetMS = &offset
	if err := repo.UpdateAnalysis(record); err != nil {
		t.Fatalf("detecting -> ready: %v", err)
	}
	// Terminal records are frozen.
	record.Confidence = 0.99
	if err := repo.UpdateAnalysis(record); err == nil {
		t.Fatal("terminal ready analysis must be immutable")
	}
	stored, err := repo.GetAnalysis("ana-01")
	if err != nil || stored.State != domain.AnalysisStateReady || stored.OffsetMS == nil {
		t.Fatalf("stored analysis is wrong: %+v err=%v", stored, err)
	}
}

func TestConformCommitIsAtomic(t *testing.T) {
	repo := newTestRepository(t)
	now := time.Now().UTC()
	record := domain.ConformJob{
		ID: "cfm-01", AnalysisID: "ana-01", TakeID: "take-01",
		State: domain.ConformStateQueued, MaxCorrectionMS: 500, MeasuredOffsetMS: 125,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, created, err := repo.CreateConform(record); err != nil || !created {
		t.Fatalf("create conform: created=%v err=%v", created, err)
	}
	// A completed bundle cannot be published directly from queued.
	if _, err := repo.CommitConform(record, []byte("zip"), []byte("receipt")); err == nil {
		t.Fatal("commit from queued must be rejected")
	}
	record.State = domain.ConformStateRendering
	if err := repo.UpdateConform(record); err != nil {
		t.Fatalf("queued -> rendering: %v", err)
	}
	record.State = domain.ConformStateCommitting
	if err := repo.UpdateConform(record); err != nil {
		t.Fatalf("rendering -> committing: %v", err)
	}
	completed, err := repo.CommitConform(record, []byte("zip-bytes"), []byte("receipt-bytes"))
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if completed.State != domain.ConformStateCompleted {
		t.Fatalf("committed conform state is %q", completed.State)
	}
	// A second commit must fail because the bundle directory already exists and
	// the record is now terminal.
	if _, err := repo.CommitConform(completed, []byte("other"), []byte("other")); err == nil {
		t.Fatal("double commit must be rejected")
	}
	if leftovers, _ := os.ReadDir(filepath.Join(repo.Root(), "tmp")); len(leftovers) != 0 {
		t.Fatalf("bundle staging directory leaked: %v", leftovers)
	}
}

func TestWriteFileAtomicReplacesDestination(t *testing.T) {
	repo := newTestRepository(t)
	path := filepath.Join(repo.Root(), "atomic.json")
	if err := writeFileAtomic(path, []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeFileAtomic(path, []byte("second")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "second" {
		t.Fatalf("atomic replace failed: %q err=%v", data, err)
	}
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	repo, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new repository: %v", err)
	}
	return repo
}
