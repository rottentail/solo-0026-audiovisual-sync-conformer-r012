// Package restart verifies persistence across an actual process interruption:
// the production syncservice binary is built, started against a data root,
// driven through real HTTP routes, killed with SIGKILL (no graceful shutdown),
// and started again on the same data root. Completed, failed and interrupted
// non-terminal records are then read back exactly as an operator would.
package restart

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/internal/domain"
	"github.com/rottentail/solo-0026-audiovisual-sync-conformer/test/synctest"
)

var portCounter int64 = 40000

type serverProcess struct {
	cmd      *exec.Cmd
	baseURL  string
	dataRoot string
	port     int
}

func nextPort() int {
	return int(atomic.AddInt64(&portCounter, 1))
}

// startServer builds (once) and launches the real syncservice binary.
func startServer(t *testing.T, dataRoot string) *serverProcess {
	t.Helper()
	binary := buildServerBinary(t)
	port := nextPort()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	cmd := exec.Command(binary, "-addr", addr, "-data", dataRoot)
	var logBuffer bytes.Buffer
	cmd.Stdout = &logBuffer
	cmd.Stderr = &logBuffer
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v\n%s", err, logBuffer.String())
	}
	process := &serverProcess{
		cmd:      cmd,
		baseURL:  "http://" + addr,
		dataRoot: dataRoot,
		port:     port,
	}
	waitForHealthy(t, process, &logBuffer)
	return process
}

func buildServerBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "syncservice")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goTool); err != nil {
		goTool = "go"
	}
	build := exec.Command(goTool, "build", "-o", binary, "./cmd/syncservice")
	build.Dir = repoRoot(t)
	output, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build server binary: %v\n%s", err, output)
	}
	return binary
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	// Tests run from <root>/test/restart; walk up to the go.mod directory.
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root")
		}
		dir = parent
	}
}

func waitForHealthy(t *testing.T, process *serverProcess, logs *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		response, err := client.Get(process.baseURL + "/healthz")
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server never became healthy\n%s", logs.String())
}

// kill simulates an abrupt crash: SIGKILL gives the process no chance to run
// graceful shutdown or flush anything beyond its already-atomic writes.
func (s *serverProcess) kill(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill server: %v", err)
	}
	state, err := s.cmd.Process.Wait()
	if err != nil && !state.Exited() {
		t.Fatalf("reap server: %v", err)
	}
}

func (s *serverProcess) request(t *testing.T, method, path string, payload any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response.StatusCode, body
}

func mustDecode(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode %q: %v", string(body), err)
	}
}

// TestRestartReadsCompletedAndInterruptedTasks is the headline restart
// scenario: a data root containing completed work, failed work, and work
// interrupted mid-flight must be re-read with every state preserved.
func TestRestartReadsCompletedAndInterruptedTasks(t *testing.T) {
	dataRoot := t.TempDir()
	fixture, err := synctest.BuildFixture(synctest.DefaultSpec())
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// ---- Process A: produce a mix of terminal and interrupted records. ----
	first := startServer(t, dataRoot)

	// Completed take + ready analysis + completed conform.
	status, body := first.request(t, http.MethodPost, "/v1/takes",
		synctest.TakePayload("take-done", "done take", fixture))
	if status != http.StatusCreated {
		t.Fatalf("admit done take: %d %s", status, body)
	}
	status, body = first.request(t, http.MethodPost, "/v1/takes/take-done/sync-analyses",
		map[string]any{"analysis_id": "ana-done", "min_confidence": 0.55, "max_offset_ms": 5000})
	if status != http.StatusCreated {
		t.Fatalf("analysis done: %d %s", status, body)
	}
	var readyAnalysis domain.SyncAnalysis
	mustDecode(t, body, &readyAnalysis)
	status, body = first.request(t, http.MethodPost, "/v1/sync-analyses/ana-done/conforms",
		map[string]any{"conform_id": "cfm-done", "max_correction_ms": 5000})
	if status != http.StatusCreated {
		t.Fatalf("conform done: %d %s", status, body)
	}
	var completedConform domain.ConformJob
	mustDecode(t, body, &completedConform)
	status, goodBundle := first.request(t, http.MethodGet, "/v1/conforms/cfm-done/bundle", nil)
	if status != http.StatusOK {
		t.Fatalf("bundle before crash: %d", status)
	}

	// A second take with a failed conform (ceiling).
	status, body = first.request(t, http.MethodPost, "/v1/takes",
		synctest.TakePayload("take-failed", "failed take", fixture))
	if status != http.StatusCreated {
		t.Fatalf("admit failed take: %d %s", status, body)
	}
	status, _ = first.request(t, http.MethodPost, "/v1/takes/take-failed/sync-analyses",
		map[string]any{"analysis_id": "ana-failed", "min_confidence": 0.55, "max_offset_ms": 5000})
	if status != http.StatusCreated {
		t.Fatalf("analysis failed-take: %d", status)
	}
	status, body = first.request(t, http.MethodPost, "/v1/sync-analyses/ana-failed/conforms",
		map[string]any{"conform_id": "cfm-failed", "max_correction_ms": 10})
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("expected ceiling failure, got %d %s", status, body)
	}

	// Interrupted records are injected as persisted non-terminal states, which
	// is exactly what an abrupt kill leaves behind between state writes.
	interruptAnalysisAndConform(t, dataRoot, "take-done")

	// Abrupt crash, no graceful shutdown.
	first.kill(t)

	// ---- Process B: restart on the same persistent data root. ----
	second := startServer(t, dataRoot)
	t.Cleanup(func() { second.kill(t) })

	// The completed take is still admitted and readable.
	status, body = second.request(t, http.MethodGet, "/v1/takes/take-done", nil)
	if status != http.StatusOK {
		t.Fatalf("take after restart: %d %s", status, body)
	}
	var restartedTake domain.MediaTake
	mustDecode(t, body, &restartedTake)
	if restartedTake.State != domain.TakeStateAdmitted {
		t.Fatalf("take state changed across restart: %s", restartedTake.State)
	}

	// The ready analysis is still terminal with the same offset.
	status, body = second.request(t, http.MethodGet, "/v1/sync-analyses/ana-done", nil)
	if status != http.StatusOK {
		t.Fatalf("ready analysis after restart: %d %s", status, body)
	}
	var restartedAnalysis domain.SyncAnalysis
	mustDecode(t, body, &restartedAnalysis)
	if restartedAnalysis.State != domain.AnalysisStateReady ||
		restartedAnalysis.OffsetMS == nil ||
		*restartedAnalysis.OffsetMS != *readyAnalysis.OffsetMS {
		t.Fatalf("ready analysis not preserved: %+v", restartedAnalysis)
	}

	// The completed conform still serves the identical bundle bytes.
	status, body = second.request(t, http.MethodGet, "/v1/conforms/cfm-done", nil)
	if status != http.StatusOK {
		t.Fatalf("conform after restart: %d", status)
	}
	var restartedConform domain.ConformJob
	mustDecode(t, body, &restartedConform)
	if restartedConform.State != domain.ConformStateCompleted {
		t.Fatalf("conform state changed across restart: %s", restartedConform.State)
	}
	status, restartedBundle := second.request(t, http.MethodGet, "/v1/conforms/cfm-done/bundle", nil)
	if status != http.StatusOK {
		t.Fatalf("bundle after restart: %d", status)
	}
	if !bytes.Equal(goodBundle, restartedBundle) {
		t.Fatal("bundle bytes changed across process restart")
	}
	// The bundle must still pass the full independent consistency verification.
	if _, err := synctest.VerifyBundle(
		restartedBundle, restartedConform, restartedAnalysis, fixture.Audio, fixture.Video,
	); err != nil {
		t.Fatalf("restarted bundle failed consistency verification: %v", err)
	}

	// The failed conform is still terminal and still exposes no bundle.
	status, body = second.request(t, http.MethodGet, "/v1/conforms/cfm-failed", nil)
	if status != http.StatusOK {
		t.Fatalf("failed conform read: %d", status)
	}
	var failedConform domain.ConformJob
	mustDecode(t, body, &failedConform)
	if failedConform.State != domain.ConformStateFailed ||
		failedConform.ErrorCode != "correction_limit_exceeded" {
		t.Fatalf("failed conform not preserved: %+v", failedConform)
	}
	if status, _ := second.request(t, http.MethodGet, "/v1/conforms/cfm-failed/bundle", nil); status != http.StatusConflict {
		t.Fatalf("failed conform exposed a bundle after restart: %d", status)
	}

	// The interrupted analysis is re-read in its persisted non-terminal state.
	status, body = second.request(t, http.MethodGet, "/v1/sync-analyses/ana-interrupted", nil)
	if status != http.StatusOK {
		t.Fatalf("interrupted analysis read: %d %s", status, body)
	}
	var stuckAnalysis domain.SyncAnalysis
	mustDecode(t, body, &stuckAnalysis)
	if stuckAnalysis.State != domain.AnalysisStateDetecting {
		t.Fatalf("interrupted analysis state not preserved: %s", stuckAnalysis.State)
	}
	// Re-posting the same analysis must not mutate or advance the frozen
	// detecting record: the service returns it unchanged.
	status, body = second.request(t, http.MethodPost, "/v1/takes/take-done/sync-analyses",
		map[string]any{"analysis_id": "ana-interrupted", "min_confidence": 0.55, "max_offset_ms": 5000})
	if status != http.StatusOK {
		t.Fatalf("re-post over detecting analysis returned %d: %s", status, body)
	}
	var rePostedAnalysis domain.SyncAnalysis
	mustDecode(t, body, &rePostedAnalysis)
	if rePostedAnalysis.State != domain.AnalysisStateDetecting || rePostedAnalysis.OffsetMS != nil {
		t.Fatalf("interrupted analysis was mutated by re-post: %+v", rePostedAnalysis)
	}
	if rePostedAnalysis.UpdatedAt != stuckAnalysis.UpdatedAt {
		t.Fatal("interrupted analysis timestamp changed across idempotent re-post")
	}

	// The interrupted rendering conform is still non-terminal and serves no
	// bundle; re-posting the same job returns it unchanged rather than
	// re-rendering or completing it.
	status, body = second.request(t, http.MethodGet, "/v1/conforms/cfm-interrupted", nil)
	if status != http.StatusOK {
		t.Fatalf("interrupted conform read: %d", status)
	}
	var stuckConform domain.ConformJob
	mustDecode(t, body, &stuckConform)
	if stuckConform.State != domain.ConformStateRendering {
		t.Fatalf("interrupted conform state not preserved: %s", stuckConform.State)
	}
	if status, _ := second.request(t, http.MethodGet, "/v1/conforms/cfm-interrupted/bundle", nil); status != http.StatusConflict {
		t.Fatalf("non-terminal conform exposed a bundle: %d", status)
	}
	status, body = second.request(t, http.MethodPost, "/v1/sync-analyses/ana-done/conforms",
		map[string]any{"conform_id": "cfm-interrupted", "max_correction_ms": 5000})
	if status != http.StatusOK {
		t.Fatalf("re-post over rendering conform returned %d: %s", status, body)
	}
	var rePostedConform domain.ConformJob
	mustDecode(t, body, &rePostedConform)
	if rePostedConform.State != domain.ConformStateRendering || rePostedConform.BundleDigest != "" {
		t.Fatalf("interrupted conform was mutated by re-post: %+v", rePostedConform)
	}

	// No staging directories survive the restart.
	tmpEntries, err := os.ReadDir(filepath.Join(dataRoot, "tmp"))
	if err != nil {
		t.Fatalf("read tmp: %v", err)
	}
	if len(tmpEntries) != 0 {
		t.Fatalf("staging directories survived restart: %v", tmpEntries)
	}
}

// TestRestartHealthAndPersistenceLayout confirms a fresh process opens an
// existing data root without clobbering namespaces.
func TestRestartHealthAndPersistenceLayout(t *testing.T) {
	dataRoot := t.TempDir()
	first := startServer(t, dataRoot)
	first.kill(t)

	second := startServer(t, dataRoot)
	defer second.kill(t)
	status, _ := second.request(t, http.MethodGet, "/healthz", nil)
	if status != http.StatusOK {
		t.Fatalf("health after restart: %d", status)
	}
	for _, namespace := range []string{"takes", "analyses", "conforms", "tmp"} {
		if info, err := os.Stat(filepath.Join(dataRoot, namespace)); err != nil || !info.IsDir() {
			t.Fatalf("namespace %s missing after restart: %v", namespace, err)
		}
	}
}
