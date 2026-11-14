package integration

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/test/synctest"
)

func TestConcurrentTakeAdmissionOneWinner(t *testing.T) {
	fixture, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	h := synctest.NewHarness(t)
	const contenders = 8
	statuses := make([]int, contenders)
	var wait sync.WaitGroup
	wait.Add(contenders)
	for index := 0; index < contenders; index++ {
		go func(i int) {
			defer wait.Done()
			// Identical requests: exactly one creates, the rest see 200.
			status, _ := h.Request(http.MethodPost, "/v1/takes",
				synctest.TakePayload("take-race", "race take", fixture))
			statuses[i] = status
		}(index)
	}
	wait.Wait()
	sort.Ints(statuses)
	created, idempotent := 0, 0
	for _, status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			idempotent++
		default:
			t.Fatalf("unexpected concurrent admission status %d", status)
		}
	}
	if created != 1 || idempotent != contenders-1 {
		t.Fatalf("expected one 201 and %d 200, got %d created and %d idempotent",
			contenders-1, created, idempotent)
	}
}

func TestConcurrentConflictingAdmission(t *testing.T) {
	base, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	h := synctest.NewHarness(t)
	statusCh := make(chan int, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		status, _ := h.Request(http.MethodPost, "/v1/takes",
			synctest.TakePayload("take-clash", "first name", base))
		statusCh <- status
	}()
	go func() {
		defer wait.Done()
		// Same id, different metadata so a conflict must be reported rather
		// than a second take silently overwriting the first.
		payload := synctest.TakePayload("take-clash", "different name", base)
		status, _ := h.Request(http.MethodPost, "/v1/takes", payload)
		statusCh <- status
	}()
	wait.Wait()
	close(statusCh)
	results := []int{<-statusCh, <-statusCh}
	sort.Ints(results)
	if results[0] != http.StatusCreated || results[1] != http.StatusConflict {
		t.Fatalf("conflicting race returned %v, want one %d and one %d",
			results, http.StatusCreated, http.StatusConflict)
	}
}

func TestConcurrentAnalysesAndConformsAreSerialized(t *testing.T) {
	fixture, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	h := synctest.NewHarness(t)
	h.AdmitTake("take-concurrent", "concurrent take", fixture)

	const workers = 6
	var analysisWait sync.WaitGroup
	analysisWait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(i int) {
			defer analysisWait.Done()
			status, _ := h.Request(http.MethodPost, "/v1/takes/take-concurrent/sync-analyses",
				map[string]any{
					"analysis_id":    "ana-shared",
					"min_confidence": 0.55,
					"max_offset_ms":  5000,
				})
			if status != http.StatusCreated && status != http.StatusOK {
				t.Errorf("concurrent analysis worker %d got status %d", i, status)
			}
		}(index)
	}
	analysisWait.Wait()

	status, analysis := h.GetAnalysis("ana-shared")
	if status != http.StatusOK || analysis.State != "ready" {
		t.Fatalf("shared analysis not ready: %d %s", status, analysis.State)
	}

	// Conforms with distinct ids all complete against the one ready analysis.
	var conformWait sync.WaitGroup
	conformWait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(i int) {
			defer conformWait.Done()
			conformID := fmt.Sprintf("cfm-%d", i)
			status, _ := h.Request(http.MethodPost, "/v1/sync-analyses/ana-shared/conforms",
				map[string]any{"conform_id": conformID, "max_correction_ms": 5000})
			if status != http.StatusCreated {
				t.Errorf("conform %s got status %d", conformID, status)
			}
		}(index)
	}
	conformWait.Wait()
	for index := 0; index < workers; index++ {
		status, conform := h.GetConform(fmt.Sprintf("cfm-%d", index))
		if status != http.StatusOK || conform.State != "completed" {
			t.Fatalf("conform %d not completed: %d %s", index, status, conform.State)
		}
	}
}
