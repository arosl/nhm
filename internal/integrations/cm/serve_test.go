package cm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeCMEnv makes the test binary a fake `cm serve` (TestHelperFakeCM):
// "ok" answers MCP initialize, "unhealthy" answers HTTP 500, "exit" exits
// at once with status 3.
const fakeCMEnv = "NHM_FAKE_CM_SERVE"

func TestHelperFakeCM(t *testing.T) {
	mode := os.Getenv(fakeCMEnv)
	if mode == "" {
		t.Skip("the fake cm serve of the supervisor tests")
	}
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	if mode == "exit" || port == "" {
		os.Exit(3)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(4)
	}
	fmt.Println("fake cm serving on", port)
	_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "unhealthy" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"cass-memory","version":"0.5.0"}}}`)
	}))
	os.Exit(5)
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// fakeServe is a config that runs the fake cm in mode, with fast probes
// and backoff.
func fakeServe(t *testing.T, mode string) ServeConfig {
	t.Helper()
	t.Setenv(fakeCMEnv, mode)
	return ServeConfig{
		ProjectDir:           t.TempDir(),
		SessionID:            "memory-serve-1",
		Port:                 freePort(t),
		Command:              []string{os.Args[0], "-test.run=^TestHelperFakeCM$", "--"},
		FirstProbe:           20 * time.Millisecond,
		HealthInterval:       100 * time.Millisecond,
		StartupHealthTimeout: 300 * time.Millisecond,
		RestartBackoffMax:    10 * time.Millisecond,
	}
}

func waitState(t *testing.T, s *Server, want ServeState) ServeStatus {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st := s.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state %s, want %s (%+v)", st.State, want, st)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fenceRecord(t *testing.T, dir string) ownershipRecord {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(PIDsDir(dir), ".daemon-*.json"))
	if len(matches) != 1 {
		t.Fatalf("fence records %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var r ownershipRecord
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// The daemon starts as starting, with its pid file; the MCP probe moves
// it to running; Shutdown ends its process, removes the pid file and
// records the fence as stopped.
func TestServeStartsProbesAndStops(t *testing.T) {
	cfg := fakeServe(t, "ok")
	s, err := StartServe(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if st.State != StateStarting || st.PID <= 0 || st.Port != cfg.Port || st.Restarts != 0 {
		t.Fatalf("after start: %+v", st)
	}
	data, err := os.ReadFile(filepath.Join(PIDsDir(cfg.ProjectDir), "cm-memory-serve-1.pid"))
	var info PIDFileInfo
	if err != nil || json.Unmarshal(data, &info) != nil || info.PID != st.PID || info.Port != cfg.Port || info.OwnerID != "memory-serve-1" {
		t.Fatalf("pid file %s, %v", data, err)
	}
	if id, err := FindSessionID(cfg.ProjectDir); err != nil || id != "memory-serve-1" {
		t.Fatalf("FindSessionID = %q, %v", id, err)
	}
	if r := fenceRecord(t, cfg.ProjectDir); r.Phase != "running" || r.PID != st.PID {
		t.Fatalf("fence while running: %+v", r)
	}
	waitState(t, s, StateRunning)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if got := s.Status().State; got != StateStopped {
		t.Fatalf("after shutdown: %s", got)
	}
	if err := syscall.Kill(st.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("the daemon still runs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(PIDsDir(cfg.ProjectDir), "cm-memory-serve-1.pid")); !os.IsNotExist(err) {
		t.Fatalf("pid file left: %v", err)
	}
	if r := fenceRecord(t, cfg.ProjectDir); r.Phase != "stopped" || r.PID != 0 {
		t.Fatalf("fence after shutdown: %+v", r)
	}
	log, _ := os.ReadFile(ServeLogPath(cfg.ProjectDir, cfg.SessionID))
	if !strings.Contains(string(log), "[supervisor] launched daemon cm (pid=") || !strings.Contains(string(log), "fake cm serving on") ||
		!strings.Contains(string(log), "health probe passed (mode=mcp); state starting -> running") {
		t.Fatalf("log:\n%s", log)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatalf("a second Shutdown: %v", err)
	}
	// The fence is free again.
	s2, err := StartServe(cfg)
	if err != nil {
		t.Fatalf("start after stop: %v", err)
	}
	_ = s2.Shutdown()
}

// A daemon whose probe keeps failing turns unhealthy after the startup
// timeout.
func TestServeTurnsUnhealthy(t *testing.T) {
	s, err := StartServe(fakeServe(t, "unhealthy"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown()
	waitState(t, s, StateUnhealthy)
}

// A daemon that keeps exiting is restarted MaxRestarts times, then fails;
// the pid file goes and the fence is released.
func TestServeRestartsThenFails(t *testing.T) {
	cfg := fakeServe(t, "exit")
	cfg.MaxRestarts = 2
	s, err := StartServe(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := waitState(t, s, StateFailed)
	if st.Restarts != 3 || st.LastError != "" {
		t.Fatalf("failed: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(PIDsDir(cfg.ProjectDir), "cm-memory-serve-1.pid")); !os.IsNotExist(err) {
		t.Fatalf("pid file left: %v", err)
	}
	log, _ := os.ReadFile(ServeLogPath(cfg.ProjectDir, cfg.SessionID))
	for _, want := range []string{"restarting in 10ms (attempt 1/2)", "restarting in 10ms (attempt 2/2)", "max restarts (2) exceeded, not restarting", "daemon exited with error: exit status 3"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("log has no %q:\n%s", want, log)
		}
	}
	if err := s.Shutdown(); err != nil || s.Status().State != StateFailed {
		t.Fatalf("shutdown after failure: %v, %s", err, s.Status().State)
	}
	if r := fenceRecord(t, cfg.ProjectDir); r.Phase != "stopped" {
		t.Fatalf("fence: %+v", r)
	}
}

// A missing binary, a busy port and a second supervisor refuse at once.
func TestServeRefusals(t *testing.T) {
	cfg := fakeServe(t, "ok")
	bad := cfg
	bad.Command = []string{"/nonexistent/cm"}
	if _, err := StartServe(bad); err == nil || !strings.Contains(err.Error(), `daemon cm: binary "/nonexistent/cm" not found`) {
		t.Fatalf("missing binary: %v", err)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("daemon cm default port %d is already in use; not starting a second instance on a random port", cfg.Port)
	if _, err := StartServe(cfg); err == nil || err.Error() != want {
		t.Fatalf("busy port: %v", err)
	}
	_ = ln.Close()
	s, err := StartServe(cfg)
	if err != nil {
		t.Fatalf("the refusals left the fence held: %v", err)
	}
	defer s.Shutdown()
	other := cfg
	other.SessionID = "memory-serve-2"
	_, err = StartServe(other)
	var oe *OwnershipError
	if !errors.Is(err, ErrDaemonOwned) || !errors.As(err, &oe) || !strings.Contains(err.Error(), "before restarting") {
		t.Fatalf("second supervisor: %v", err)
	}
	if _, err := StartServe(ServeConfig{ProjectDir: cfg.ProjectDir, SessionID: "a/b"}); err == nil || err.Error() != `invalid session ID "a/b"` {
		t.Fatalf("bad session: %v", err)
	}
	// A Server that never started, or whose start failed, shuts down at
	// once.
	idle, err := NewServer(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := idle.Start(); err == nil {
		t.Fatal("a missing binary started")
	}
	if err := idle.Shutdown(); err != nil {
		t.Fatalf("shutdown of a failed start: %v", err)
	}
}

// A record that an earlier supervisor left in the launching phase, or
// running with a live pid, needs a person: no launch over it.
func TestServeRefusesAnUncertainEarlierLaunch(t *testing.T) {
	cfg := fakeServe(t, "ok")
	if err := os.MkdirAll(PIDsDir(cfg.ProjectDir), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(PIDsDir(cfg.ProjectDir), fmt.Sprintf(".daemon-%x.json", sha256Of("cm")))
	for phase, want := range map[string]string{
		`"phase":"launching","pid":0`:                          "previous launch outcome is unknown",
		fmt.Sprintf(`"phase":"running","pid":%d`, os.Getpid()): "previous daemon process or process group may still be alive",
		`"phase":"stopped","pid":12`:                           "stopped ownership record retains a PID",
	} {
		rec := `{"version":1,"name":"cm","owner_id":"memory-serve-0",` + phase + `}`
		if err := os.WriteFile(path, []byte(rec), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := StartServe(cfg)
		if !errors.Is(err, ErrDaemonRecoveryRequired) || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v", phase, err)
		}
	}
}

func sha256Of(s string) [32]byte { return sha256.Sum256([]byte(s)) }

// The restart backoff doubles from 1 s up to its cap.
func TestServeBackoff(t *testing.T) {
	s := &Server{cfg: ServeConfig{RestartBackoffMax: 60 * time.Second}}
	var got []time.Duration
	for attempt := 1; attempt <= 8; attempt++ {
		got = append(got, s.backoff(attempt))
	}
	if fmt.Sprint(got) != "[1s 2s 4s 8s 16s 32s 1m0s 1m0s]" {
		t.Fatalf("backoff = %v", got)
	}
}
