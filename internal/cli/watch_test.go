package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arosl/nhm/internal/herdr/herdrtest"
)

// watch prints the lines above each pane's live region: all of them in
// the first round, then each line whose text the last read did not hold.
// The fake answers as herdr does, with the last lines asked for. The live
// region's spinner and its last line change on every read, and neither
// prints. A task line that the agent redraws above the live region prints
// once, with its new text; blank lines are left out. --lines counts the
// lines above the live region, so watch asks for 10 more (nhm-rnx).
func TestWatchPrintsEachLineOnce(t *testing.T) {
	s, _, _ := logsServer(t, func(string) string { return "" })
	var mu sync.Mutex
	var asked []int
	reads := 0
	s.Handle("pane.read", func(r herdrtest.Request) (any, *herdrtest.Error) {
		var p struct {
			PaneID string `json:"pane_id"`
			Lines  int    `json:"lines"`
		}
		_ = json.Unmarshal(r.Params, &p)
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, p.Lines)
		reads++
		// The output grows by a line each read, above a task line that is
		// ticked from the third read; then the live region.
		task := "☐ task A"
		if reads >= 3 {
			task = "☒ task A"
		}
		lines := strings.Split(strings.TrimSuffix(numbered(reads), "\n"), "\n")
		lines = append(lines, "", task, fmt.Sprintf("✻ Working… (%ds)", reads))
		for range logsLiveLines - 2 {
			lines = append(lines, "│ box │")
		}
		lines = append(lines, fmt.Sprintf("status %d", reads))
		lines = lines[max(0, len(lines)-p.Lines):]
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": p.PaneID, "tab_id": "w1:t1",
			"workspace_id": "w1", "source": "recent", "format": "text", "revision": 3,
			"text": "\x1b[1m" + strings.Join(lines, "\n") + "\x1b[0m\n", "truncated": false}}, nil
	})
	code, out, errOut := run("watch", "swarm", "IcyKnoll", "--lines", "4", "--count", "4", "--interval", "1ms", "--socket", s.SocketPath)
	want := "IcyKnoll | line 1\nIcyKnoll | ☐ task A\nIcyKnoll | line 2\nIcyKnoll | line 3\nIcyKnoll | ☒ task A\nIcyKnoll | line 4\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("watch = %d\n%s%s\nwant\n%s", code, out, errOut, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(asked) != "[14 14 14 14]" {
		t.Fatalf("lines asked = %v, want 4 and the live region's 10 in each read", asked)
	}
}
