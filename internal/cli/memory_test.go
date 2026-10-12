package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/cm"
)

// The cli tests never run the host's cm or cass: both are "not
// installed" unless a test sets a fake.
func init() {
	cmRunner = func(string) cm.Runner {
		return func(context.Context, ...string) ([]byte, error) { return nil, cm.ErrNotInstalled }
	}
	cassRunner = func(string) cass.Runner {
		return func(context.Context, ...string) ([]byte, error) { return nil, cass.ErrNotInstalled }
	}
}

// fakeTools answers cm and cass by subcommand and records the calls.
type fakeTools struct {
	mu    sync.Mutex
	calls []string
}

const ruleAnswer = `{"success":true,"data":{"task":"q","relevantBullets":[{"id":"b-login","category":"testing",
"content":"Run the login tests after an auth change."}],"antiPatterns":[]}}`

const hitAnswer = `{"hits":[{"title":"Earlier login fix","content":"We fixed the login redirect.","score":2.5,
"source_path":"/h/.claude/projects/-p/old.jsonl","agent":"claude_code","line_number":4,"created_at":1791777605000}]}`

func (f *fakeTools) use(t *testing.T) {
	t.Helper()
	cmOld, cassOld := cmRunner, cassRunner
	t.Cleanup(func() { cmRunner, cassRunner = cmOld, cassOld })
	record := func(tool string, args []string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, tool+" "+strings.Join(args, " "))
	}
	cmRunner = func(string) cm.Runner {
		return func(_ context.Context, args ...string) ([]byte, error) {
			record("cm", args)
			if args[0] == "context" {
				return []byte(ruleAnswer), nil
			}
			return []byte(`{"success":true,"data":{}}`), nil
		}
	}
	cassRunner = func(string) cass.Runner {
		return func(_ context.Context, args ...string) ([]byte, error) {
			record("cass", args)
			return []byte(hitAnswer), nil
		}
	}
}

func (f *fakeTools) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestMemoryShowsTheBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	(&fakeTools{}).use(t)
	code, out, errOut := run("memory", "fix", "the", "login", "--project", t.TempDir())
	if code != 0 || !strings.Contains(out, "[nhm context:") || !strings.Contains(out, "[b-login] (testing) Run the login tests") ||
		!strings.Contains(out, "included cass /h/.claude/projects/-p/old.jsonl:4") {
		t.Fatalf("memory = %d %s %s", code, out, errOut)
	}
	code, out, _ = run("memory", "q", "--cm", "required", "--cass", "off", "--json")
	res := decodeEnvelope(t, out)["result"].(map[string]any)
	if code != 0 || res["cass"].(map[string]any)["enabled"] != false {
		t.Fatalf("json = %d %s", code, out)
	}
	if code, _, _ = run("memory"); code != exitRefused {
		t.Fatalf("no query = %d", code)
	}
	if code, _, _ = run("memory", "q", "--cm", "always"); code != exitRefused {
		t.Fatalf("bad mode = %d", code)
	}
}

func TestMemoryRequiredWithoutCMFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	code, _, errOut := run("memory", "q", "--cm", "required", "--cass", "off")
	if code != exitError || !strings.Contains(errOut, "cm is not installed") {
		t.Fatalf("required = %d %s", code, errOut)
	}
}

func TestSendWithMemoryAndFeedback(t *testing.T) {
	s, inst, h := newSendInstance(t)
	ownNhm(t, inst)
	f := &fakeTools{}
	f.use(t)
	sock := "--socket=" + s.SocketPath
	code, out, errOut := run("send", "swarm", "Fix the login bug.", "--cc", "--with-memory", "--op-id", "op-m1", sock)
	if code != 0 {
		t.Fatalf("send = %d %s %s", code, out, errOut)
	}
	sent := h.sent()
	if len(sent) != 1 || !strings.Contains(sent[0], "[b-login]") || !strings.HasSuffix(sent[0], "\n\nFix the login bug.") {
		t.Fatalf("prompt = %q", sent)
	}
	// The duplicate check is on by default and only warns.
	if f.count("cass search") != 1 || !strings.Contains(errOut, "cass-check: 1 earlier session") {
		t.Fatalf("check: calls %q, stderr %s", f.calls, errOut)
	}
	if code, out, _ = run("feedback", "list", "--name", "IcyKnoll", sock); code != 0 || !strings.Contains(out, "op-m1 IcyKnoll (submitted)") ||
		!strings.Contains(out, "  b-login v") {
		t.Fatalf("list = %d %s", code, out)
	}
	code, out, errOut = run("feedback", "mark", "op-m1", "b-login", "--helpful", sock)
	if code != 0 || !strings.Contains(out, "b-login marked helpful for IcyKnoll") || f.count("cm mark b-login --helpful") != 1 {
		t.Fatalf("mark = %d %s %s %q", code, out, errOut, f.calls)
	}
	if code, out, _ = run("feedback", "mark", "op-m1", "b-login", "--helpful", sock); code != 0 || !strings.Contains(out, "before") ||
		f.count("cm mark") != 1 {
		t.Fatalf("repeat = %d %s", code, out)
	}
	if code, _, _ = run("feedback", "mark", "op-m1", "b-login", "--harmful", sock); code != exitRefused {
		t.Fatalf("opposite = %d", code)
	}
	if code, out, _ = run("feedback", "outcomes", sock); code != 0 || !strings.Contains(out, "learning profile is off") {
		t.Fatalf("outcomes = %d %s", code, out)
	}
	// Conflicting flags refuse before anything is sent.
	if code, _, _ = run("send", "swarm", "x", "--with-cass", "--no-cass", sock); code != exitRefused || len(h.sent()) != 1 {
		t.Fatalf("conflict = %d", code)
	}
}

func TestFindTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	claude := filepath.Join(home, ".claude", "projects", "-p", "sid-1.jsonl")
	codex := filepath.Join(home, ".codex", "sessions", "2026", "10", "12", "rollout-2026-10-12T04-00-00-sid-2.jsonl")
	for _, p := range []string{claude, codex} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := findTranscript("claude", "sid-1"); err != nil || got != claude {
		t.Fatalf("claude = %q, %v", got, err)
	}
	if got, err := findTranscript("codex", "sid-2"); err != nil || got != codex {
		t.Fatalf("codex = %q, %v", got, err)
	}
	if got, _ := findTranscript("pi", "sid-1"); got != "" {
		t.Fatalf("pi = %q", got)
	}
	if _, err := findTranscript("claude", "../x"); err == nil {
		t.Fatal("a path in a session id was accepted")
	}
}
