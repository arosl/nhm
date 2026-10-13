// Derived from ntm (github.com/Dicklesworthstone/ntm) internal/tools/xf_test.go
// at commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: this header; nhm's names; only the search test is kept (ntm's
// health tests go with its health check); the fake-binary tests are nhm's.

package xf

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// indexTestXFArchive indexes a two-tweet X export into an isolated
// XF_DB/XF_INDEX with the installed xf, leaving the operator's archive alone.
func indexTestXFArchive(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xf"); err != nil {
		t.Skip("xf not installed")
	}

	root := t.TempDir()
	data := filepath.Join(root, "archive", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	tweets := `window.YTD.tweets.part0 = [
  {"tweet": {"id_str": "1234567890123456789", "created_at": "Wed Jan 08 12:00:00 +0000 2025",
    "full_text": "Hello world! This is a test tweet about Rust programming. #rust",
    "favorite_count": "42", "retweet_count": "7", "lang": "en",
    "entities": {"hashtags": [{"text": "rust"}], "user_mentions": [], "urls": []}}},
  {"tweet": {"id_str": "1234567890123456790", "created_at": "Thu Jan 09 14:30:00 +0000 2025",
    "full_text": "Learning about Tantivy search engine. It is fast for full-text search!",
    "favorite_count": "100", "retweet_count": "25", "lang": "en",
    "entities": {"hashtags": [], "user_mentions": [], "urls": []}}}
]`
	manifest := `window.YTD.manifest.part0 = {
  "userInfo": {"accountId": "999999999", "userName": "test_user", "displayName": "Test User"},
  "archiveInfo": {"sizeBytes": "1234", "generationDate": "2025-01-01T00:00:00Z", "isPartialArchive": false}
}`
	for name, content := range map[string]string{"tweets.js": tweets, "manifest.js": manifest} {
		if err := os.WriteFile(filepath.Join(data, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("XF_DB", filepath.Join(root, "xf.db"))
	t.Setenv("XF_INDEX", filepath.Join(root, "index"))
	if out, err := exec.Command("xf", "index", filepath.Join(root, "archive")).CombinedOutput(); err != nil {
		t.Fatalf("xf index: %v\n%s", err, out)
	}
}

func TestXFSearchReadsInstalledXF(t *testing.T) {
	indexTestXFArchive(t)
	adapter := NewAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	results, err := adapter.Search(ctx, "rust", SearchParams{Limit: 2})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	var found *SearchResult
	for i := range results {
		if results[i].ID == "1234567890123456789" {
			found = &results[i]
		}
	}
	if found == nil {
		t.Fatalf("Search() = %+v, want the Rust tweet", results)
	}
	if !strings.Contains(found.Text, "Rust programming") || found.ResultType != "tweet" ||
		found.CreatedAt != "2025-01-08T12:00:00Z" || found.Score <= 0 {
		t.Fatalf("Search() decoded %+v, want xf's text/result_type/created_at/score", *found)
	}

	// A query starting with "-" is xf query syntax (exclusion), not a flag.
	if _, err := adapter.Search(ctx, "-rust tantivy", SearchParams{Limit: 2}); err != nil {
		t.Fatalf("Search() with a dash-leading query: %v", err)
	}

	// Lexical search with no match prints nothing on stdout; that is zero
	// results, not a parse failure.
	none, err := adapter.Search(ctx, "zzznomatch", SearchParams{Mode: "lexical"})
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("Search(lexical, no match) = %+v, %v; want empty results", none, err)
	}
}

// fakeXF writes an xf that records its arguments and prints out (and
// exits with code).
func fakeXF(t *testing.T, out string, code int) (*Adapter, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	outFile := filepath.Join(dir, "out")
	if err := os.WriteFile(outFile, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argsFile + "'\ncat '" + outFile + "'\necho 'xf: oops' >&2\nexit " +
		strconv.Itoa(code) + "\n"
	bin := filepath.Join(dir, "xf")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Adapter{Binary: bin, Timeout: 10 * time.Second}, argsFile
}

func TestSearchArgumentsAndOutput(t *testing.T) {
	a, argsFile := fakeXF(t, `[{"id":"1","text":"hi","created_at":"2025-01-01","result_type":"tweet","score":1.5}]`, 0)
	if path, ok := a.Detect(); !ok || path != a.Binary {
		t.Fatalf("Detect() = %q %v", path, ok)
	}
	got, err := a.Search(context.Background(), "-rust", SearchParams{Limit: 20, Mode: "lexical", Sort: "date"})
	if err != nil || len(got) != 1 || got[0].Text != "hi" || got[0].Score != 1.5 {
		t.Fatalf("Search() = %+v %v", got, err)
	}
	args, _ := os.ReadFile(argsFile)
	if want := "search\n--format\njson\n--limit\n20\n--mode\nlexical\n--sort\ndate\n--\n-rust\n"; string(args) != want {
		t.Fatalf("args:\n%s\nwant:\n%s", args, want)
	}
}

func TestSearchFailures(t *testing.T) {
	a, _ := fakeXF(t, "", 0)
	if got, err := a.Search(context.Background(), "q", SearchParams{}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no output = %+v %v", got, err)
	}
	a, _ = fakeXF(t, "not json", 0)
	if _, err := a.Search(context.Background(), "q", SearchParams{}); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("bad output: %v", err)
	}
	a, _ = fakeXF(t, "", 1)
	if _, err := a.Search(context.Background(), "q", SearchParams{}); err == nil || !strings.Contains(err.Error(), "xf: oops") {
		t.Fatalf("failure: %v", err)
	}
	a, _ = fakeXF(t, strings.Repeat("x", 64), 0)
	defer func(old int) { outputLimit = old }(outputLimit)
	outputLimit = 16
	if _, err := a.Search(context.Background(), "q", SearchParams{}); !errors.Is(err, ErrOutputLimitExceeded) {
		t.Fatalf("too much output: %v", err)
	}
	if _, ok := (&Adapter{Binary: filepath.Join(t.TempDir(), "none")}).Detect(); ok {
		t.Fatal("a missing xf was detected")
	}
}

func TestSearchTimeout(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "xf")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{Binary: bin, Timeout: 200 * time.Millisecond}
	start := time.Now()
	if _, err := a.Search(context.Background(), "q", SearchParams{}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the timeout took %s", time.Since(start))
	}
}
