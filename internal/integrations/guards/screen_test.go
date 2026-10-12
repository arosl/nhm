package guards

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDCG installs a dcg that answers with body and exit code, and
// records its stdin.
func fakeDCG(t *testing.T, body string, code int) string {
	t.Helper()
	dir := t.TempDir()
	stdin := filepath.Join(dir, "stdin")
	script := "#!/bin/sh\ncat > '" + stdin + "'\nprintf '%s' '" + body + "'\nexit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dcg"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	old := DCGPath
	DCGPath = filepath.Join(dir, "dcg")
	t.Cleanup(func() { DCGPath = old })
	return stdin
}

func TestScreenArgv(t *testing.T) {
	stdin := fakeDCG(t, `{"decision":"allow"}`, 0)
	v, err := ScreenArgv(context.Background(), []string{"claude", "--model", "opus", "--append-system-prompt", "it's fine"})
	if err != nil || v.Decision != Allow {
		t.Fatalf("allow = %+v %v", v, err)
	}
	got, _ := os.ReadFile(stdin)
	if string(got) != `claude --model opus --append-system-prompt 'it'\''s fine'` {
		t.Fatalf("stdin = %q", got)
	}

	fakeDCG(t, `{"decision":"deny","rule_id":"core.git:reset-hard","reason":"no"}`, 1)
	v, err = ScreenArgv(context.Background(), []string{"git", "reset", "--hard"})
	if err != nil || v.Decision != Deny || v.RuleID != "core.git:reset-hard" {
		t.Fatalf("deny = %+v %v", v, err)
	}
	fakeDCG(t, `{"decision":"warn","reason":"careful"}`, 2)
	if v, _ := ScreenArgv(context.Background(), []string{"x"}); v.Decision != Deny || !strings.Contains(v.Reason, "warn") {
		t.Fatalf("warn = %+v", v)
	}
	fakeDCG(t, `not json`, 3)
	if _, err := ScreenArgv(context.Background(), []string{"x"}); err == nil {
		t.Fatal("a broken answer passed")
	}
	DCGPath = filepath.Join(t.TempDir(), "no-dcg")
	if _, err := ScreenArgv(context.Background(), []string{"x"}); !errors.Is(err, ErrNoDCG) {
		t.Fatalf("no dcg = %v", err)
	}
	if _, err := ScreenArgv(context.Background(), nil); err == nil {
		t.Fatal("empty argv passed")
	}
}

func TestShellJoin(t *testing.T) {
	if got := ShellJoin([]string{"codex", "--no-daemon", "-c", "model=o3", "", "a b", "$HOME"}); got != `codex --no-daemon -c model=o3 '' 'a b' '$HOME'` {
		t.Fatalf("ShellJoin = %s", got)
	}
}
