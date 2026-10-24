// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/supervisor/supervisor.go (the daemon states, the cm entry of
// DefaultSpecs, Start, monitorDaemon, waitForExit, handleDaemonFailure,
// restartBackoff, stopDaemon, writePIDFile, isPortAvailable),
// internal/supervisor/health.go (probeDaemonMCP),
// internal/supervisor/ownership.go and ownership_unix.go (the ownership
// fence: acquireDaemonOwnership, its record and phases, saveLocked,
// prepare, started, startFailed, finishWithCleanup, lockDaemonOwnership,
// daemonProcessMayLive) and procattr_unix.go at commit 9d5ed977; see
// LICENSE in this directory and PROVENANCE.md. Local changes: it runs cm
// only, one daemon per Server, and one goroutine owns each generation's
// lifecycle where ntm's locks share it; the pid, log and fence files are
// under <project>/.nhm/ (the operator's ruling, 2026-10-24); the command is
// a field, so tests run a fake cm.
package cm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ServeState is the state of the supervised cm daemon, in ntm's words.
type ServeState string

const (
	StateStarting   ServeState = "starting"
	StateRunning    ServeState = "running"
	StateStopping   ServeState = "stopping"
	StateStopped    ServeState = "stopped"
	StateFailed     ServeState = "failed"
	StateRestarting ServeState = "restarting"
	// StateUnhealthy: the process lives, but its health probe has not
	// passed within the startup timeout, or stopped passing.
	StateUnhealthy ServeState = "unhealthy"
)

// ntm's defaults for the cm daemon.
const (
	DefaultServePort   = 8200
	DefaultMaxRestarts = 5
)

var (
	// ErrDaemonOwned: another supervisor holds this project's daemon.
	ErrDaemonOwned = errors.New("daemon is owned by another supervisor")
	// ErrDaemonRecoveryRequired: an earlier launch may still run, so no
	// new one starts until a person looks.
	ErrDaemonRecoveryRequired = errors.New("daemon ownership requires inspection")
)

// ServeConfig is a supervised `cm serve`. The zero durations and counts
// are ntm's defaults.
type ServeConfig struct {
	ProjectDir string // the pid, log and fence files go under its .nhm/
	SessionID  string // names the pid and log files: cm-<session>.pid
	Port       int    // 8200; a busy port refuses, never a random one
	// Command is the cm binary and any arguments before "serve"
	// (default: cm).
	Command              []string
	HealthInterval       time.Duration // 5 s
	FirstProbe           time.Duration // 2 s
	StartupHealthTimeout time.Duration // 30 s
	MaxRestarts          int           // 5
	RestartBackoffMax    time.Duration // 60 s
}

// ServeStatus is a snapshot of the daemon.
type ServeStatus struct {
	State     ServeState
	PID       int
	Port      int
	Restarts  int
	LastError string
}

// Server supervises one cm daemon: it restarts it with backoff when it
// exits, probes its health, and stops it on Shutdown.
type Server struct {
	cfg     ServeConfig
	owner   *ownership
	pidPath string
	logPath string

	mu         sync.Mutex
	st         ServeStatus
	gen        *generation
	startedAt  time.Time
	lastHealth time.Time

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

// generation is one launched cm process.
type generation struct {
	cmd    *exec.Cmd
	log    *os.File
	exited chan struct{}
	err    error // cmd.Wait's error, once exited is closed
}

// StartServe is NewServer, then Start.
func StartServe(cfg ServeConfig) (*Server, error) {
	s, err := NewServer(cfg)
	if err != nil {
		return nil, err
	}
	if err := s.Start(); err != nil {
		return nil, err
	}
	return s, nil
}

// NewServer checks the config, fills in ntm's defaults and makes the pids
// and logs directories. It starts nothing.
func NewServer(cfg ServeConfig) (*Server, error) {
	if cfg.Port <= 0 {
		cfg.Port = DefaultServePort
	}
	if len(cfg.Command) == 0 {
		cfg.Command = []string{"cm"}
	}
	if cfg.HealthInterval < 100*time.Millisecond {
		cfg.HealthInterval = 5 * time.Second
	}
	if cfg.FirstProbe <= 0 {
		cfg.FirstProbe = 2 * time.Second
	}
	if cfg.StartupHealthTimeout <= 0 {
		cfg.StartupHealthTimeout = 30 * time.Second
	}
	if cfg.MaxRestarts == 0 {
		cfg.MaxRestarts = DefaultMaxRestarts
	}
	if cfg.RestartBackoffMax <= 0 {
		cfg.RestartBackoffMax = 60 * time.Second
	}
	if cfg.ProjectDir == "" {
		return nil, errors.New("project directory required")
	}
	if err := validateComponent(cfg.SessionID, "session ID"); err != nil {
		return nil, err
	}
	for _, dir := range []string{PIDsDir(cfg.ProjectDir), LogsDir(cfg.ProjectDir)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return &Server{
		cfg:     cfg,
		pidPath: filepath.Join(PIDsDir(cfg.ProjectDir), fmt.Sprintf("cm-%s.pid", cfg.SessionID)),
		logPath: ServeLogPath(cfg.ProjectDir, cfg.SessionID),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

// Start takes the project's cm fence and launches the daemon. A missing
// binary, a busy port and a held fence refuse at once. A Server starts
// once.
func (s *Server) Start() error {
	if s.owner != nil {
		return errors.New("daemon cm already managed")
	}
	owner, err := acquireOwnership(PIDsDir(s.cfg.ProjectDir), "cm", s.cfg.SessionID)
	if err != nil {
		return err
	}
	s.owner = owner
	gen, err := s.launch(0)
	if err != nil {
		s.owner = nil
		return errors.Join(err, owner.finish(nil))
	}
	go s.run(gen)
	return nil
}

// ServeLogPath is the log of a session's cm daemon.
func ServeLogPath(projectDir, sessionID string) string {
	return filepath.Join(LogsDir(projectDir), fmt.Sprintf("cm-%s.log", sessionID))
}

// Status is a snapshot of the daemon.
func (s *Server) Status() ServeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st
}

// launch starts one generation of the daemon in the starting state.
func (s *Server) launch(restarts int) (*generation, error) {
	bin := s.cfg.Command[0]
	if !filepath.IsAbs(bin) {
		if _, err := exec.LookPath(bin); err != nil {
			return nil, fmt.Errorf("daemon cm: binary %q not found in PATH: %w", bin, err)
		}
	} else if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("daemon cm: binary %q not found: %w", bin, err)
	}
	// A busy port almost always means another cm already serves this
	// store: a second one on a random port would share its database.
	if !portAvailable(s.cfg.Port) {
		return nil, fmt.Errorf("daemon cm default port %d is already in use; not starting a second instance on a random port", s.cfg.Port)
	}
	log, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	dir, err := filepath.Abs(s.cfg.ProjectDir)
	if err != nil {
		_ = log.Close()
		return nil, fmt.Errorf("resolve daemon working directory: %w", err)
	}
	args := append(append([]string(nil), s.cfg.Command[1:]...), "serve", "--port", strconv.Itoa(s.cfg.Port))
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Dir = dir
	// Its own process group, so a stop reaches cm's children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The launch is journalled before exec: after a crash of nhm, an
	// unlocked fence does not prove that no daemon runs.
	if err := s.owner.prepare(); err != nil {
		_ = log.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return nil, errors.Join(fmt.Errorf("start daemon cm: %w", err), s.owner.startFailed())
	}
	gen := &generation{cmd: cmd, log: log, exited: make(chan struct{})}
	go func() {
		gen.err = cmd.Wait()
		close(gen.exited)
	}()
	if err := s.owner.started(cmd.Process.Pid); err != nil {
		// A child whose pid is not journalled must not keep running.
		gen.terminate()
		_ = log.Close()
		s.setState(func(st *ServeStatus) { st.State, st.LastError = StateFailed, err.Error() })
		return nil, err
	}
	now := time.Now()
	s.mu.Lock()
	s.gen = gen
	s.startedAt = now
	s.st = ServeStatus{State: StateStarting, PID: cmd.Process.Pid, Port: s.cfg.Port, Restarts: restarts}
	s.mu.Unlock()
	fmt.Fprintf(log, "[supervisor] launched daemon cm (pid=%d port=%d cmd=%s state=%s)\n", cmd.Process.Pid, s.cfg.Port, bin, StateStarting)
	if err := s.writePIDFile(cmd.Process.Pid, now); err != nil {
		fmt.Fprintf(log, "[supervisor] warning: failed to write PID file: %v\n", err)
	}
	return gen, nil
}

// run owns the daemon until Shutdown: it probes each generation's health,
// and restarts one that exits, with backoff, up to MaxRestarts.
func (s *Server) run(gen *generation) {
	defer close(s.done)
	for {
		if !s.watch(gen) {
			return // Shutdown stops the generation
		}
		if gen.err != nil {
			fmt.Fprintf(gen.log, "[supervisor] daemon exited with error: %v\n", gen.err)
		}
		restarts := s.Status().Restarts
		next := gen
		for next == gen {
			restarts++
			if restarts > s.cfg.MaxRestarts {
				err := s.owner.finish(s.removePIDFile)
				fmt.Fprintf(gen.log, "[supervisor] max restarts (%d) exceeded, not restarting\n", s.cfg.MaxRestarts)
				s.setState(func(st *ServeStatus) {
					st.State, st.Restarts = StateFailed, restarts
					if err != nil {
						st.LastError = err.Error()
					}
				})
				_ = gen.log.Close()
				return
			}
			backoff := s.backoff(restarts)
			s.setState(func(st *ServeStatus) { st.State, st.Restarts = StateRestarting, restarts })
			fmt.Fprintf(gen.log, "[supervisor] restarting in %v (attempt %d/%d)\n", backoff, restarts, s.cfg.MaxRestarts)
			timer := time.NewTimer(backoff)
			select {
			case <-s.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
			g, err := s.launch(restarts)
			switch {
			case err == nil:
				next = g
			case errors.Is(err, ErrDaemonRecoveryRequired):
				err = errors.Join(err, s.owner.finish(nil))
				s.setState(func(st *ServeStatus) { st.State, st.LastError = StateFailed, err.Error() })
				_ = gen.log.Close()
				return
			default:
				// A busy port, a binary that is gone, a failed exec:
				// each takes one try of the same budget.
				fmt.Fprintf(gen.log, "[supervisor] restart failed: %v\n", err)
			}
		}
		_ = gen.log.Close()
		gen = next
	}
}

// watch probes a generation's health until it exits (true) or Shutdown
// begins (false). As ntm's: the probes come on the health ticker, which
// starts with the generation, and none before FirstProbe.
func (s *Server) watch(gen *generation) bool {
	ticker := time.NewTicker(s.cfg.HealthInterval)
	defer ticker.Stop()
	first := time.NewTimer(s.cfg.FirstProbe)
	defer first.Stop()
	select {
	case <-s.stop:
		return false
	case <-gen.exited:
		return s.exitedNotStopped()
	case <-first.C:
	}
	for {
		select {
		case <-s.stop:
			return false
		case <-gen.exited:
			return s.exitedNotStopped()
		case <-ticker.C:
			s.probe(gen)
		}
	}
}

// exitedNotStopped: a generation that exits while Shutdown runs is not a
// crash.
func (s *Server) exitedNotStopped() bool {
	select {
	case <-s.stop:
		return false
	default:
		return true
	}
}

// probe runs one health probe and moves the state: starting or
// unhealthy to running when it passes; starting or running to unhealthy
// when it has failed past the startup timeout.
func (s *Server) probe(gen *generation) {
	err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return probeMCP(ctx, fmt.Sprintf("http://127.0.0.1:%d/", s.cfg.Port))
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.st.State
	if err == nil {
		s.lastHealth = time.Now()
		if prev == StateStarting || prev == StateUnhealthy {
			s.st.State = StateRunning
			fmt.Fprintf(gen.log, "[supervisor] daemon cm health probe passed (mode=mcp); state %s -> %s\n", prev, StateRunning)
		}
		return
	}
	since := time.Since(s.startedAt)
	if prev == StateRunning {
		since = time.Since(s.lastHealth)
	}
	if (prev == StateStarting || prev == StateRunning) && since > s.cfg.StartupHealthTimeout {
		s.st.State = StateUnhealthy
		fmt.Fprintf(gen.log, "[supervisor] daemon cm health probe failing for %v (timeout %v); state %s -> %s\n",
			since.Round(time.Millisecond), s.cfg.StartupHealthTimeout, prev, StateUnhealthy)
	}
}

// Shutdown stops the daemon (SIGTERM to its process group, SIGKILL after
// 2 s), removes its pid file and releases the fence. It is safe to call
// more than once.
func (s *Server) Shutdown() error {
	if s.owner == nil {
		return nil // never started
	}
	var err error
	s.stopOnce.Do(func() {
		s.setState(func(st *ServeStatus) {
			if st.State != StateFailed {
				st.State = StateStopping
			}
		})
		close(s.stop)
		<-s.done
		s.mu.Lock()
		gen := s.gen
		s.mu.Unlock()
		if gen != nil {
			gen.terminate()
			// The process is reaped before its log closes, so its last
			// output stays. run may have closed it already.
			_ = gen.log.Close()
		}
		err = s.owner.finish(s.removePIDFile)
		s.setState(func(st *ServeStatus) {
			if st.State != StateFailed {
				st.State = StateStopped
			}
			if err != nil {
				st.LastError = err.Error()
			}
		})
	})
	return err
}

// terminate asks the generation's process group to end and waits for the
// process: SIGTERM, then SIGKILL after 2 s.
func (g *generation) terminate() {
	select {
	case <-g.exited:
		return
	default:
	}
	pid := g.cmd.Process.Pid
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		_ = g.cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-g.exited:
	case <-time.After(2 * time.Second):
		if syscall.Kill(-pid, syscall.SIGKILL) != nil {
			_ = g.cmd.Process.Kill()
		}
		<-g.exited
	}
}

func (s *Server) setState(f func(*ServeStatus)) {
	s.mu.Lock()
	f(&s.st)
	s.mu.Unlock()
}

// backoff doubles from 1 s up to RestartBackoffMax.
func (s *Server) backoff(attempt int) time.Duration {
	b := time.Second
	for i := 1; i < attempt && b < s.cfg.RestartBackoffMax; i++ {
		if b > s.cfg.RestartBackoffMax/2 {
			return s.cfg.RestartBackoffMax
		}
		b *= 2
	}
	return min(b, s.cfg.RestartBackoffMax)
}

func (s *Server) writePIDFile(pid int, started time.Time) error {
	data, err := json.MarshalIndent(PIDFileInfo{PID: pid, Port: s.cfg.Port, OwnerID: s.cfg.SessionID,
		Command: s.cfg.Command[0], StartedAt: started}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.pidPath, data, 0o644)
}

func (s *Server) removePIDFile() { _ = os.Remove(s.pidPath) }

func portAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

var healthClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		ResponseHeaderTimeout: 3 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		MaxIdleConnsPerHost:   2,
	},
}

const maxHealthResponse = 1 << 20

// probeMCP is the daemon's health probe: an MCP initialize at its root
// that answers JSON-RPC 2.0, id 1, no error and a server name.
func probeMCP(ctx context.Context, url string) error {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"nhm-supervisor","version":"health-probe"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := healthClient.Do(req)
	if err != nil {
		return errors.Join(ctx.Err(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon health HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxHealthResponse+1))
	if err != nil {
		return errors.Join(ctx.Err(), fmt.Errorf("read daemon health response: %w", err))
	}
	if len(data) > maxHealthResponse {
		return errors.New("daemon health response exceeds size limit")
	}
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return errors.New("daemon health response is not a complete JSON-RPC object")
	}
	if env.JSONRPC != "2.0" || !bytes.Equal(bytes.TrimSpace(env.ID), []byte("1")) {
		return errors.New("daemon health response does not match the initialize request")
	}
	if len(env.Error) > 0 && !bytes.Equal(bytes.TrimSpace(env.Error), []byte("null")) {
		return errors.New("daemon health initialize returned a JSON-RPC error")
	}
	var result struct {
		ServerInfo *struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil || result.ServerInfo == nil || result.ServerInfo.Name == "" {
		return errors.New("daemon health response has no initialize server identity")
	}
	return ctx.Err()
}

func validateComponent(value, kind string) error {
	if strings.TrimSpace(value) == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("invalid %s %q", kind, value)
	}
	return nil
}

// OwnershipError names the record that an earlier supervisor left. It
// never permits a signal to the recorded pid: the pid may be reused.
type OwnershipError struct {
	Name    string
	Path    string
	OwnerID string
	PID     int
	Cause   error
}

func (e *OwnershipError) Error() string {
	return fmt.Sprintf("daemon %q: %v (owner=%q pid=%d; inspect %s before restarting)", e.Name, e.Cause, e.OwnerID, e.PID, e.Path)
}

func (e *OwnershipError) Unwrap() error { return e.Cause }

const (
	ownershipVersion  = 1
	maxOwnershipBytes = 64 << 10
)

type ownershipRecord struct {
	Version    int       `json:"version"`
	Name       string    `json:"name"`
	OwnerID    string    `json:"owner_id"`
	Supervisor int       `json:"supervisor_pid"`
	PID        int       `json:"pid"`
	Phase      string    `json:"phase"` // launching, running, stopped
	UpdatedAt  time.Time `json:"updated_at"`
}

// ownership is the fence of one daemon name in a pids directory: a flock
// on a lock file that is never unlinked, and a record of the launch phase
// beside it, replaced atomically. Every generation of one Server uses the
// same lease.
type ownership struct {
	mu       sync.Mutex
	lock     *os.File
	lockPath string
	path     string
	record   ownershipRecord
}

func acquireOwnership(pidsDir, name, owner string) (*ownership, error) {
	if err := validateComponent(name, "daemon name"); err != nil {
		return nil, err
	}
	// A canonical path gives a project's symlinked aliases one fence.
	dir, err := filepath.EvalSymlinks(pidsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve daemon ownership directory: %w", err)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}
	key := fmt.Sprintf(".daemon-%x", sha256.Sum256([]byte(name)))
	o := &ownership{
		lockPath: filepath.Join(dir, key+".lock"),
		path:     filepath.Join(dir, key+".json"),
		record: ownershipRecord{Version: ownershipVersion, Name: name, OwnerID: owner,
			Supervisor: os.Getpid(), Phase: "stopped"},
	}
	if o.lock, err = lockOwnership(o.lockPath); err != nil {
		return nil, &OwnershipError{Name: name, Path: o.path, Cause: err}
	}
	ok := false
	defer func() {
		if !ok {
			_ = o.closeLocked()
		}
	}()
	prev, err := readOwnership(o.path)
	if err != nil {
		return nil, o.recoveryError(err)
	}
	if prev != nil {
		if prev.Version != ownershipVersion || prev.Name != name || prev.OwnerID == "" || prev.PID < 0 {
			return nil, o.recoveryError(errors.New("invalid or mismatched ownership record"))
		}
		switch prev.Phase {
		case "stopped":
			if prev.PID != 0 {
				return nil, o.recoveryError(errors.New("stopped ownership record retains a PID"))
			}
		case "running":
			if prev.PID == 0 {
				return nil, o.recoveryError(errors.New("running ownership record has no PID"))
			}
			if live, perr := processMayLive(prev.PID); live || perr != nil {
				return nil, &OwnershipError{Name: name, Path: o.path, OwnerID: prev.OwnerID, PID: prev.PID,
					Cause: errors.Join(ErrDaemonRecoveryRequired, perr, errors.New("previous daemon process or process group may still be alive"))}
			}
		case "launching":
			// The earlier owner may have died after exec and before it
			// saved the child's pid.
			return nil, &OwnershipError{Name: name, Path: o.path, OwnerID: prev.OwnerID,
				Cause: errors.Join(ErrDaemonRecoveryRequired, errors.New("previous launch outcome is unknown"))}
		default:
			return nil, o.recoveryError(errors.New("unknown ownership phase"))
		}
	}
	ok = true
	return o, nil
}

func lockOwnership(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = errors.New("daemon ownership fence is not a regular file")
	}
	if err == nil {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(ErrDaemonOwned, err)
		}
		return nil, err
	}
	return f, nil
}

// processMayLive: the process or its group may still run. A dead leader
// can leave children on the same store.
func processMayLive(pid int) (bool, error) {
	if pid <= 0 {
		return true, errors.New("invalid daemon PID")
	}
	for _, id := range []int{pid, -pid} {
		err := syscall.Kill(id, 0)
		if err == nil || errors.Is(err, syscall.EPERM) {
			return true, nil
		}
		if !errors.Is(err, syscall.ESRCH) {
			return true, err
		}
	}
	return false, nil
}

func readOwnership(path string) (*ownershipRecord, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("ownership record is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("ownership record changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOwnershipBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOwnershipBytes {
		return nil, errors.New("ownership record exceeds size limit")
	}
	var r ownershipRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("ownership record contains trailing data")
	}
	return &r, nil
}

func (o *ownership) recoveryError(err error) error {
	return &OwnershipError{Name: o.record.Name, Path: o.path, OwnerID: o.record.OwnerID, PID: o.record.PID,
		Cause: errors.Join(ErrDaemonRecoveryRequired, err)}
}

func (o *ownership) saveLocked() error {
	if o.lock == nil {
		return o.recoveryError(errors.New("ownership fence is closed"))
	}
	opened, err := o.lock.Stat()
	current, serr := os.Lstat(o.lockPath)
	if err != nil || serr != nil || !os.SameFile(opened, current) {
		return o.recoveryError(errors.New("ownership fence was replaced"))
	}
	o.record.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(o.record)
	if err != nil {
		return err
	}
	dir := filepath.Dir(o.path)
	f, err := os.CreateTemp(dir, ".daemon-record-*")
	if err != nil {
		return o.recoveryError(err)
	}
	defer os.Remove(f.Name())
	_, werr := f.Write(data)
	err = errors.Join(werr, f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), o.path)
	}
	if err == nil {
		err = syncDir(dir)
	}
	if err != nil {
		return o.recoveryError(err)
	}
	return nil
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// prepare records the launch before exec. It stores no command or
// environment: they can hold secrets.
func (o *ownership) prepare() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.record.Phase == "launching" {
		return o.recoveryError(errors.New("previous launch has not been finalized"))
	}
	if o.record.PID > 0 {
		if live, err := processMayLive(o.record.PID); live || err != nil {
			return o.recoveryError(errors.Join(err, errors.New("previous process group has not exited")))
		}
	}
	o.record.Phase, o.record.PID = "launching", 0
	return o.saveLocked()
}

func (o *ownership) started(pid int) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.record.Phase, o.record.PID = "running", pid
	return o.saveLocked()
}

// startFailed: exec failed, so no child exists.
func (o *ownership) startFailed() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.record.Phase, o.record.PID = "stopped", 0
	return o.saveLocked()
}

// finish releases the fence once the record says the daemon is stopped.
// When the daemon may still run, it keeps the running or launching record,
// so that no new supervisor launches over it, and still closes the lock.
// cleanup runs before the lock is released.
func (o *ownership) finish(cleanup func()) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lock == nil {
		return nil
	}
	var err error
	if o.record.Phase == "launching" {
		err = o.recoveryError(errors.New("launch outcome has not been recorded"))
	} else if o.record.PID > 0 {
		if live, perr := processMayLive(o.record.PID); live || perr != nil {
			err = o.recoveryError(errors.Join(perr, errors.New("daemon process or process group may still be alive")))
		}
	}
	if err == nil {
		o.record.Phase, o.record.PID = "stopped", 0
		err = o.saveLocked()
	}
	if err == nil && cleanup != nil {
		cleanup()
	}
	return errors.Join(err, o.closeLocked())
}

func (o *ownership) closeLocked() error {
	if o.lock == nil {
		return nil
	}
	err := o.lock.Close() // closing releases the flock; never unlink it
	o.lock = nil
	return err
}
