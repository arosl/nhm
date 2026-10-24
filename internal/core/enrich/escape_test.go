package enrich

import (
	"strings"
	"testing"
)

// CM and CASS text can hold terminal text; the block is typed as one
// paste, so clean keeps no control character: an ESC [201~ would end
// herdr's paste (nhm-ubi).
func TestCleanDropsControlCharacters(t *testing.T) {
	got, _ := clean("Read the log\x1b[201~\r!rm -rf x\x7f\x00 done")
	if strings.IndexFunc(got, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 || got != "Read the log [201~ !rm -rf x done" {
		t.Fatalf("clean = %q", got)
	}
	if q := QueryOf("fix\x1b[31m the bug"); q != "fix [31m the bug" {
		t.Fatalf("query = %q", q)
	}
}
