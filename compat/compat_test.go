package compat

import "testing"

func TestRowsParseAndUseKnownDispositions(t *testing.T) {
	rows := Rows()
	if len(rows) < 20 {
		t.Fatalf("rows = %d", len(rows))
	}
	for _, r := range rows {
		switch r.Disposition {
		case "implemented", "deferred", "waived":
		default:
			t.Errorf("%s: disposition %q", r.Form, r.Disposition)
		}
		if r.Disposition != "implemented" && r.Note == "" {
			t.Errorf("%s: a %s row names its bead or ruling", r.Form, r.Disposition)
		}
	}
	// A deferred row is work still owed: none is fine (nhm-k9d answered
	// the last ones).
	c := Counts(rows)
	if c["implemented"] == 0 || c["waived"] == 0 {
		t.Fatalf("counts = %v", c)
	}
}
