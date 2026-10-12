package agentmail

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 12, 4, 0, 0, 0, time.UTC)

func TestPaneKeyMatchesAgentMail(t *testing.T) {
	// The cases of Agent Mail's own sanitize tests, plus herdr's ids.
	for in, want := range map[string]string{
		"%3":           "3",
		"main:0:2":     "main-0-2",
		"w1:p2":        "w1-p2",
		"wH:pR":        "wH-pR",
		"a/b c":        "a_b_c",
		"":             "unknown",
		"%":            "unknown",
		"plain_id-01":  "plain_id-01",
		"../../escape": "______escape",
	} {
		if got := PaneKey(in); got != want {
			t.Errorf("PaneKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectHashIsSHA1OfTheCanonicalPath(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(real))
	want := hex.EncodeToString(sum[:])[:12]
	if got := ProjectHash(real); got != want {
		t.Fatalf("ProjectHash = %s, want %s", got, want)
	}
	if got := ProjectHash(link); got != want {
		t.Fatalf("a symlinked key hashes to %s, want %s", got, want)
	}
	if got := ProjectHash("relative/key"); len(got) != 12 {
		t.Fatalf("relative key hash %q", got)
	}
}

func TestWriteReadRemove(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	path, err := WriteIdentity(home, project, "w1:p2", "IcyKnoll", t0)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "agent-mail", "identity", ProjectHash(project), "w1-p2")
	if path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"name":"IcyKnoll","pane_id":"w1:p2","written_at":"2026-10-12T04:00:00Z"}`+"\n" {
		t.Fatalf("content = %q", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}
	rec, ok, err := ReadIdentity(home, project, "w1:p2")
	if err != nil || !ok || rec.Name != "IcyKnoll" || rec.PaneID != "w1:p2" {
		t.Fatalf("read = %+v %v %v", rec, ok, err)
	}
	if removed, err := RemoveIdentity(home, project, "w1:p2", "OtherName"); err != nil || removed {
		t.Fatalf("removed another name's file: %v %v", removed, err)
	}
	if removed, err := RemoveIdentity(home, project, "w1:p2", "icyknoll"); err != nil || !removed {
		t.Fatalf("remove = %v %v", removed, err)
	}
	if _, ok, err := ReadIdentity(home, project, "w1:p2"); ok || err != nil {
		t.Fatalf("file still there: %v %v", ok, err)
	}
}

func TestReadLegacyAndEmpty(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	path, err := IdentityPath(home, project, "%3")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for content, want := range map[string]string{"BlueLake\n": "BlueLake", "  \n": "", `{"name":"  "}`: ""} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		rec, ok, err := ReadIdentity(home, project, "%3")
		if err != nil || ok != (want != "") || rec.Name != want {
			t.Errorf("%q: read = %+v %v %v", content, rec, ok, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"name":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadIdentity(home, project, "%3"); err == nil {
		t.Error("broken JSON read without an error")
	}
}

func TestSymlinksAreRefused(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "agent-mail"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "agent-mail", "identity")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIdentity(home, project, "w1:p1", "IcyKnoll", t0); !errors.Is(err, ErrSymlink) {
		t.Fatalf("write through a symlink: %v", err)
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("created %s through the symlink", entries[0].Name())
	}
}

func TestInvalidNameIsRefused(t *testing.T) {
	if _, err := WriteIdentity(t.TempDir(), t.TempDir(), "w1:p1", "Two\nLines", t0); err == nil {
		t.Fatal("a name with a newline was written")
	}
}

func TestConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/config")
	if got, err := ConfigHome(); err != nil || got != "/x/config" {
		t.Fatalf("ConfigHome = %q %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/x/home")
	if got, err := ConfigHome(); err != nil || got != "/x/home/.config" {
		t.Fatalf("ConfigHome = %q %v", got, err)
	}
}
