package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Key is one key `nhm bind` writes: a herdr key and the plugin action it
// runs.
type Key struct {
	Key    string
	Action string
	What   string
}

// Keys are nhm's keys (the operator, 2026-10-12): no F keys; prefix+a,
// prefix+d and prefix+m are free in herdr 0.9.3's defaults (prefix+s is
// herdr's settings).
var Keys = []Key{
	{Key: "prefix+a", Action: "nhm.palette", What: "the command palette"},
	{Key: "prefix+d", Action: "nhm.board", What: "the agent board"},
	{Key: "prefix+m", Action: "nhm.menu", What: "the nhm menu"},
}

// The markers of the block `nhm bind` manages, and of the block ACFS's
// `acfs palette bind` wrote before nhm took over the palette.
const (
	BlockBegin     = "# >>> nhm >>>"
	BlockEnd       = "# <<< nhm <<<"
	acfsBlockBegin = "# >>> acfs palette >>>"
	acfsBlockEnd   = "# <<< acfs palette <<<"
)

// ConfigPath is herdr's config.toml: $HERDR_CONFIG_PATH, else
// $XDG_CONFIG_HOME/herdr/config.toml, else ~/.config/herdr/config.toml.
func ConfigPath(getenv func(string) string) (string, error) {
	if p := getenv("HERDR_CONFIG_PATH"); p != "" {
		return p, nil
	}
	if d := getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "herdr", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("ui: no home directory for herdr's config")
	}
	return filepath.Join(home, ".config", "herdr", "config.toml"), nil
}

// Block is the text `nhm bind` writes.
func Block() string {
	var b strings.Builder
	b.WriteString(BlockBegin + "\n")
	b.WriteString("# Written by `nhm bind`; `nhm bind --unbind` removes it. Edits inside are lost.\n")
	for _, k := range Keys {
		fmt.Fprintf(&b, "\n# %s: %s\n[[keys.command]]\nkey = %q\ntype = \"plugin_action\"\ncommand = %q\n", k.Key, k.What, k.Key, k.Action)
	}
	b.WriteString(BlockEnd + "\n")
	return b.String()
}

// withoutBlock removes a marked block (and the blank line after it).
func withoutBlock(text, begin, end string) (string, error) {
	var out []string
	inside, skipBlank := false, false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case !inside && strings.TrimSpace(line) == begin:
			inside = true
		case inside && strings.TrimSpace(line) == end:
			inside, skipBlank = false, true
		case inside:
		case skipBlank && strings.TrimSpace(line) == "":
			skipBlank = false
		default:
			skipBlank = false
			out = append(out, line)
		}
	}
	if inside {
		return "", fmt.Errorf("ui: %q has no %q; fix herdr's config by hand", begin, end)
	}
	return strings.Join(out, "\n"), nil
}

// Unbind returns the config without nhm's block and without ACFS's old
// palette block.
func Unbind(config string) (string, error) {
	text, err := withoutBlock(config, BlockBegin, BlockEnd)
	if err != nil {
		return "", err
	}
	return withoutBlock(text, acfsBlockBegin, acfsBlockEnd)
}

// Bind returns the config with nhm's block in place of any earlier one,
// ACFS's palette block removed, and the block appended at the end. It
// refuses when the rest of the config already uses one of nhm's keys.
func Bind(config string) (string, error) {
	rest, err := Unbind(config)
	if err != nil {
		return "", err
	}
	for _, k := range Keys {
		re := regexp.MustCompile(`(?m)^\s*[A-Za-z_]+\s*=\s*"` + regexp.QuoteMeta(k.Key) + `"`)
		if loc := re.FindString(rest); loc != "" {
			return "", fmt.Errorf("ui: herdr's config already binds %s (%s); free it, then run nhm bind again", k.Key, strings.TrimSpace(loc))
		}
	}
	rest = strings.TrimRight(rest, "\n")
	if rest != "" {
		rest += "\n\n"
	}
	return rest + Block(), nil
}

// ClientNote is what `nhm bind --print-client` prints with the block: a
// remote client uses its own key map (herdr 0.9.3 docs, "Persistence and
// remote access").
const ClientNote = `# herdr --remote uses the client's own key map: add this block to the
# config.toml of the herdr client (on your Mac), or attach with
#   herdr --remote <host> --remote-keybindings server
# to use the server's keys. Without either, type in any server pane:
#   herdr plugin action invoke nhm.menu
`
