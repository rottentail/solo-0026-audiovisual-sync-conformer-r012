package restart

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
)

// interruptAnalysisAndConform writes non-terminal analysis and conform records
// straight into the data root. This reproduces the on-disk state left by an
// abrupt process kill after the "detecting"/"rendering" state was persisted but
// before the workflow reached its terminal state, without having to time the
// kill against in-memory processing.
func interruptAnalysisAndConform(t *testing.T, dataRoot, takeID string) {
	t.Helper()
	now := time.Now().UTC()

	stuckAnalysis := domain.SyncAnalysis{
		ID:        "ana-interrupted",
		TakeID:    takeID,
		State:     domain.AnalysisStateDetecting,
		Policy:    domain.SyncPolicy{MinConfidence: 0.55, MaxOffsetMS: 5000},
		CreatedAt: now,
		UpdatedAt: now,
	}
	writeRecord(t, filepath.Join(dataRoot, "analyses", "ana-interrupted.json"), stuckAnalysis)

	stuckConform := domain.ConformJob{
		ID:               "cfm-interrupted",
		AnalysisID:       "ana-done",
		TakeID:           takeID,
		State:            domain.ConformStateRendering,
		MaxCorrectionMS:  5000,
		MeasuredOffsetMS: 125,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	writeRecord(t, filepath.Join(dataRoot, "conforms", "cfm-interrupted.json"), stuckConform)
}

func writeRecord(t *testing.T, path string, record any) {
	t.Helper()
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatalf("encode interrupted record: %v", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create record directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write interrupted record: %v", err)
	}
}
