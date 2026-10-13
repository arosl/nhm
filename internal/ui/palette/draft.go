package palette

import (
	"errors"
	"maps"
	"strings"
)

// ErrEmptyDraft refuses a payload with no text. The palette shows it in
// the editor; it never sends the command's prompt instead.
var ErrEmptyDraft = errors.New("the message is empty: type something to send, or esc to cancel")

// Draft is the text the palette would send, kept apart from the command
// it started from (plan-v4 sections 4 and 5). It is present once the
// user has a text of their own, even an empty one: an emptied draft
// stays empty and is refused, and it never falls back to the command's
// prompt. (ntm's palette kept the edit in a string and read "" as "no
// edit", so an emptied draft sent the template.)
type Draft struct {
	present bool
	text    string
	rev     uint64
}

// Set makes text the draft.
func (d *Draft) Set(text string) {
	if d.present && d.text == text {
		return
	}
	d.present, d.text = true, text
	d.rev++
}

// Reset drops the draft, so the command's prompt applies again: another
// command was picked.
func (d *Draft) Reset() {
	d.present, d.text = false, ""
	d.rev++
}

// Present reports whether the draft holds the user's own text.
func (d Draft) Present() bool { return d.present }

// Rev is the draft's revision. It changes with every Set that changes
// the text and with every Reset, so a preview made for one revision is
// never shown for another.
func (d Draft) Rev() uint64 { return d.rev }

// Text is what would be sent for cmd: the draft when present, else the
// command's prompt.
func (d Draft) Text(cmd *PaletteCmd) string {
	if d.present || cmd == nil {
		return d.text
	}
	return cmd.Prompt
}

// Payload is what one submission sends.
type Payload struct {
	// Key is the command's key; "" for a message with no command.
	Key  string
	Text string
	// Template: the text is a catalog prompt, rendered as a template, so
	// its placeholders are filled per target as `nhm send -t` fills them.
	// Otherwise it goes as typed (a custom message, an xf result).
	Template bool
	// Vars fill a template's placeholders (--var).
	Vars map[string]string
	// Rev is the draft's revision.
	Rev uint64
}

// Payload builds the payload for cmd with vars, or ErrEmptyDraft. A
// command from a catalog is a template.
func (d Draft) Payload(cmd *PaletteCmd, vars map[string]string) (Payload, error) {
	text := d.Text(cmd)
	if strings.TrimSpace(text) == "" {
		return Payload{}, ErrEmptyDraft
	}
	p := Payload{Text: text, Rev: d.rev, Vars: maps.Clone(vars)}
	if cmd != nil {
		p.Key, p.Template = cmd.Key, cmd.Source != ""
	}
	return p, nil
}
