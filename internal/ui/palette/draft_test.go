package palette

import (
	"errors"
	"testing"
)

// ntm's bug: an emptied draft was read as "no draft" and sent the
// template. Here it stays empty and is refused.
func TestAnEmptiedDraftIsRefused(t *testing.T) {
	cmd := &PaletteCmd{Key: "review", Prompt: "Review the diff.", Source: "/c.md"}
	var d Draft
	if d.Present() || d.Text(cmd) != "Review the diff." {
		t.Fatalf("no draft: text = %q", d.Text(cmd))
	}
	d.Set("")
	if !d.Present() || d.Text(cmd) != "" {
		t.Fatalf("emptied draft: present = %v, text = %q", d.Present(), d.Text(cmd))
	}
	if _, err := d.Payload(cmd, nil); !errors.Is(err, ErrEmptyDraft) {
		t.Fatalf("emptied draft: %v", err)
	}
	d.Set(" \n\t")
	if _, err := d.Payload(cmd, nil); !errors.Is(err, ErrEmptyDraft) {
		t.Fatalf("blank draft: %v", err)
	}
	d.Reset()
	if d.Present() || d.Text(cmd) != "Review the diff." {
		t.Fatal("a reset draft gives the command's prompt again")
	}
}

func TestDraftRevisions(t *testing.T) {
	var d Draft
	r0 := d.Rev()
	d.Set("a")
	r1 := d.Rev()
	d.Set("a")
	if d.Rev() != r1 || r1 == r0 {
		t.Fatalf("revs %d %d %d: a change bumps, the same text does not", r0, r1, d.Rev())
	}
	d.Set("")
	r2 := d.Rev()
	d.Reset()
	if r2 == r1 || d.Rev() == r2 {
		t.Fatal("emptying and resetting are changes")
	}
}

func TestPayloads(t *testing.T) {
	catalog := &PaletteCmd{Key: "fix", Prompt: "Fix {{thread}}.", Source: "/c.md"}
	var d Draft
	vars := map[string]string{"thread": "nhm-1"}
	p, err := d.Payload(catalog, vars)
	if err != nil || !p.Template || p.Key != "fix" || p.Text != "Fix {{thread}}." || p.Vars["thread"] != "nhm-1" || p.Rev != d.Rev() {
		t.Fatalf("catalog payload = %+v %v", p, err)
	}
	vars["thread"] = "changed"
	if p.Vars["thread"] != "nhm-1" {
		t.Fatal("the payload keeps its own variables")
	}
	// A custom message and an xf result have no catalog: sent as typed.
	compose := &PaletteCmd{Key: "custom-message", Label: "Custom message"}
	d.Set("hello {{not a var}}")
	p, err = d.Payload(compose, nil)
	if err != nil || p.Template || p.Text != "hello {{not a var}}" {
		t.Fatalf("custom payload = %+v %v", p, err)
	}
}
