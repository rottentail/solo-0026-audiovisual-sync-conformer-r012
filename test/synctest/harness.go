package synctest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/httpapi"
	appservice "github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/service"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/storage"
)

// Harness wires the real HTTP handler to an in-process repository backed by a
// temporary data root. Tests talk exclusively through JSON HTTP routes, so the
// whole production transport, service, media and storage stack is exercised.
type Harness struct {
	t      *testing.T
	Root   string
	server *httptest.Server
	client *http.Client
	repo   *storage.Repository
}

// NewHarness creates a fresh harness with its own data root.
func NewHarness(t *testing.T) *Harness {
	t.Helper()
	root := t.TempDir()
	repository, err := storage.New(root)
	if err != nil {
		t.Fatalf("initialize repository: %v", err)
	}
	api := httpapi.New(appservice.New(repository))
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)
	return &Harness{
		t:      t,
		Root:   root,
		server: server,
		client: &http.Client{Timeout: 30 * time.Second},
		repo:   repository,
	}
}

// Repository exposes the storage repository for direct persistence assertions.
func (h *Harness) Repository() *storage.Repository {
	return h.repo
}

// URL returns the loopback base URL.
func (h *Harness) URL() string {
	return h.server.URL
}

// TakePayload builds an admit request body for a fixture.
func TakePayload(id, name string, fixture Fixture) map[string]any {
	return map[string]any{
		"take_id":      id,
		"name":         name,
		"note":         "automated verification fixture",
		"audio_base64": base64.StdEncoding.EncodeToString(fixture.Audio),
		"video_base64": base64.StdEncoding.EncodeToString(fixture.Video),
	}
}

// AdmitTake POSTs a fixture and asserts success.
func (h *Harness) AdmitTake(id, name string, fixture Fixture) domain.MediaTake {
	h.t.Helper()
	status, body := h.Request(http.MethodPost, "/v1/takes", TakePayload(id, name, fixture))
	if status != http.StatusCreated {
		h.t.Fatalf("admit take %s returned %d: %s", id, status, body)
	}
	var take domain.MediaTake
	mustDecode(h.t, body, &take)
	return take
}

// CreateAnalysis runs the analysis workflow and returns the terminal record.
func (h *Harness) CreateAnalysis(takeID, analysisID string, minConfidence float64, maxOffsetMS int) (int, domain.SyncAnalysis) {
	h.t.Helper()
	status, body := h.Request(
		http.MethodPost,
		fmt.Sprintf("/v1/takes/%s/sync-analyses", takeID),
		map[string]any{
			"analysis_id":    analysisID,
			"min_confidence": minConfidence,
			"max_offset_ms":  maxOffsetMS,
		},
	)
	var analysis domain.SyncAnalysis
	mustDecode(h.t, body, &analysis)
	return status, analysis
}

// CreateConform runs the conform workflow and returns the HTTP status and record.
func (h *Harness) CreateConform(analysisID, conformID string, maxCorrectionMS int) (int, domain.ConformJob) {
	h.t.Helper()
	status, body := h.Request(
		http.MethodPost,
		fmt.Sprintf("/v1/sync-analyses/%s/conforms", analysisID),
		map[string]any{
			"conform_id":        conformID,
			"max_correction_ms": maxCorrectionMS,
		},
	)
	var conform domain.ConformJob
	mustDecode(h.t, body, &conform)
	return status, conform
}

// GetAnalysis reads an analysis through its public route.
func (h *Harness) GetAnalysis(analysisID string) (int, domain.SyncAnalysis) {
	h.t.Helper()
	status, body := h.Request(http.MethodGet, fmt.Sprintf("/v1/sync-analyses/%s", analysisID), nil)
	var analysis domain.SyncAnalysis
	mustDecode(h.t, body, &analysis)
	return status, analysis
}

// GetConform reads a conform job through its public route.
func (h *Harness) GetConform(conformID string) (int, domain.ConformJob) {
	h.t.Helper()
	status, body := h.Request(http.MethodGet, fmt.Sprintf("/v1/conforms/%s", conformID), nil)
	var conform domain.ConformJob
	mustDecode(h.t, body, &conform)
	return status, conform
}

// GetBundle downloads the ZIP bundle.
func (h *Harness) GetBundle(conformID string) (int, []byte) {
	h.t.Helper()
	status, body := h.Request(http.MethodGet, fmt.Sprintf("/v1/conforms/%s/bundle", conformID), nil)
	return status, body
}

// Request performs a JSON HTTP call and returns the status code and raw body.
func (h *Harness) Request(method, path string, payload any) (int, []byte) {
	h.t.Helper()
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			h.t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatalf("perform %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		h.t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, body
}

// ErrorCode extracts the stable error code from an error envelope.
func ErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode error envelope %q: %v", string(body), err)
	}
	return envelope.Error.Code
}

func mustDecode(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response %q: %v", string(body), err)
	}
}
