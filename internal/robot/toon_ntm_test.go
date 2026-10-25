// Taken in part from ntm (github.com/Dicklesworthstone/ntm)
// internal/robot/toon_pure_test.go and toon_test.go (TestFilepathBase,
// TestLooksLikeToonRustVersion, TestToonDelimiterArg) at commit 9d5ed977;
// see LICENSE in this directory and PROVENANCE.md. Local changes: this
// header; ntm's tests that need a tru binary are left out; the tests
// after "nhm's" below are nhm's (nhm-2p6.2.11).

package robot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestToonIsIdentifierStart(t *testing.T) {

	tests := []struct {
		name string
		c    rune
		want bool
	}{
		{"lowercase a", 'a', true},
		{"lowercase z", 'z', true},
		{"uppercase A", 'A', true},
		{"uppercase Z", 'Z', true},
		{"underscore", '_', true},
		{"digit 0", '0', false},
		{"digit 9", '9', false},
		{"space", ' ', false},
		{"hyphen", '-', false},
		{"dot", '.', false},
		{"at sign", '@', false},
		{"unicode letter", 'é', false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toonIsIdentifierStart(tc.c)
			if got != tc.want {
				t.Errorf("toonIsIdentifierStart(%q) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

func TestToonIsIdentifierChar(t *testing.T) {

	tests := []struct {
		name string
		c    rune
		want bool
	}{
		{"lowercase a", 'a', true},
		{"uppercase Z", 'Z', true},
		{"underscore", '_', true},
		{"digit 0", '0', true},
		{"digit 9", '9', true},
		{"space", ' ', false},
		{"hyphen", '-', false},
		{"dot", '.', false},
		{"at sign", '@', false},
		{"unicode letter", 'ü', false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toonIsIdentifierChar(tc.c)
			if got != tc.want {
				t.Errorf("toonIsIdentifierChar(%q) = %v, want %v", tc.c, got, tc.want)
			}
		})
	}
}

func TestFilepathBase(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple filename", "file.txt", "file.txt"},
		{"unix path", "/usr/bin/tru", "tru"},
		{"windows path", `C:\Program Files\tru.exe`, "tru.exe"},
		{"mixed slashes", `C:\Users/bin\tru`, "tru"},
		{"trailing slash unix", "/usr/bin/", ""},
		{"just filename no path", "binary", "binary"},
		{"empty string", "", ""},
		{"root slash", "/", ""},
		{"relative path", "foo/bar/baz", "baz"},
		{"double slashes", "//foo//bar", "bar"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filepathBase(tt.input)
			if got != tt.want {
				t.Errorf("filepathBase(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLooksLikeToonRustVersion(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"tru version", "tru 0.1.0", true},
		{"tru version 1.x", "tru 1.2.3", true},
		{"tru with suffix", "tru 0.1.0-beta", true},
		{"coreutils tr", "tr (gnu coreutils) 9.1", false},
		{"empty string", "", false},
		{"just tru", "tru", false},
		{"tru space no version", "tru ", false},
		{"toon_rust prefix", "toon_rust 0.1.0", false},
		{"wrong format", "version 1.0", false},
		{"tru with letter after space", "tru abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := looksLikeToonRustVersion(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeToonRustVersion(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestToonDelimiterArg(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty string", "", ","},
		{"comma", ",", ","},
		{"tab character", "\t", "tab"},
		{"pipe", "|", "|"},
		{"tab keyword", "tab", "tab"},
		{"comma keyword", "comma", "comma"},
		{"pipe keyword", "pipe", "pipe"},
		{"whitespace trimmed", "  custom  ", "custom"},
		{"semicolon", ";", ";"},
		{"colon", ":", ":"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toonDelimiterArg(tt.input)
			if got != tt.want {
				t.Errorf("toonDelimiterArg(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// nhm's: the pure Go encoder joins tab-delimited values with a tab, where
// ntm's writes tru's flag word "tab" there.
func TestToonPureGoJoinsWithTheDelimiter(t *testing.T) {
	type row struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	got, err := toonEncodePureGo([]row{{"a", 1}, {"b", 2}}, "\t")
	if err != nil || got != "[2]{name,n}:\n a\t1\n b\t2\n" {
		t.Fatalf("tabular = %q %v", got, err)
	}
	if got, err := toonEncodePureGo([]int{1, 2, 3}, "\t"); err != nil || got != "[3]:1\t2\t3\n" {
		t.Fatalf("primitive array = %q %v", got, err)
	}
	if got, err := toonEncodePureGo(map[string]any{"ok": true, "s": "a b"}, "\t"); err != nil || got != "ok: true\ns: \"a b\"\n" {
		t.Fatalf("object = %q %v", got, err)
	}
}

// nhm's: with toon_rust's tru (TOON_TRU_BIN), the payload's JSON goes to
// `tru --encode --delimiter tab` and tru's answer is the output; a
// TOON_TRU_BIN that is not tru falls back to the pure Go encoder.
func TestRenderTOONWithTru(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake tru is a POSIX shell script")
	}
	dir := t.TempDir()
	tru := filepath.Join(dir, "tru")
	script := "#!/bin/sh\ncase \"$1\" in\n--help) echo 'toon_rust: the reference implementation in Rust' ;;\n" +
		"*) printf 'TRU %s %s %s:' \"$1\" \"$2\" \"$3\"; cat ;;\nesac\n"
	if err := os.WriteFile(tru, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOON_BIN", "")
	t.Setenv("TOON_TRU_BIN", tru)
	got, err := RenderTOON(map[string]any{"ok": true})
	if err != nil || got != `TRU --encode --delimiter tab:{"ok":true}` {
		t.Fatalf("with tru = %q %v", got, err)
	}
	notTru := filepath.Join(dir, "other")
	if err := os.WriteFile(notTru, []byte("#!/bin/sh\necho nothing\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOON_TRU_BIN", notTru)
	if got, err := RenderTOON(map[string]any{"ok": true}); err != nil || !strings.HasPrefix(got, "ok: true") {
		t.Fatalf("with a TOON_TRU_BIN that is not tru = %q %v", got, err)
	}
}
