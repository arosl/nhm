package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
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

// multiline returns the delimiter of the multi-line string a TOML line
// leaves open (three double or three single quotes), or "" for none,
// given the one open before it.
func multiline(open, line string) string {
	for i := 0; i < len(line); {
		rest := line[i:]
		if open != "" {
			j := closeIndex(rest, open)
			if j < 0 {
				return open
			}
			i, open = i+j, ""
			continue
		}
		switch {
		case rest[0] == '#':
			return ""
		case strings.HasPrefix(rest, `"""`), strings.HasPrefix(rest, `'''`):
			open, i = rest[:3], i+3
		case rest[0] == '"':
			// A basic string on one line: skip to its unescaped end.
			j := 1
			for j < len(rest) && rest[j] != '"' {
				if rest[j] == '\\' {
					j++
				}
				j++
			}
			i += j + 1
		case rest[0] == '\'':
			j := strings.IndexByte(rest[1:], '\'')
			if j < 0 {
				return ""
			}
			i += j + 2
		default:
			i++
		}
	}
	return open
}

// closeIndex returns the index just past the delimiter that closes a
// multi-line string in s, or -1 when s does not close it. In a basic
// string (""") a backslash escapes the next character, so \""" does not
// close it. Up to two quotes before the delimiter belong to the string
// (TOML: """a""""" is a"" closed), so the closing run is taken whole.
func closeIndex(s, delim string) int {
	for k := 0; k < len(s); k++ {
		if delim == `"""` && s[k] == '\\' {
			k++
			continue
		}
		if strings.HasPrefix(s[k:], delim) {
			end := k + len(delim)
			for n := 0; n < 2 && end < len(s) && s[end] == delim[0]; n++ {
				end++
			}
			return end
		}
	}
	return -1
}

// codeOf is a TOML line without its comment: the text before the first #
// outside a string. A line that opens a multi-line string is kept whole.
func codeOf(line string) string {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '#':
			return line[:i]
		case '"', '\'':
			if strings.HasPrefix(line[i:], `"""`) || strings.HasPrefix(line[i:], `'''`) {
				return line
			}
			q := line[i]
			for i++; i < len(line) && line[i] != q; i++ {
				if q == '"' && line[i] == '\\' {
					i++
				}
			}
		}
	}
	return line
}

// withoutBlock removes a marked block (and the blank line after it). A
// marker counts only at the top level, never inside a multi-line string.
func withoutBlock(text, begin, end string) (string, error) {
	var out []string
	inside, skipBlank, open := false, false, ""
	for _, line := range strings.Split(text, "\n") {
		inString := open != ""
		open = multiline(open, line)
		switch {
		case inString && !inside:
			skipBlank = false
			out = append(out, line)
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

// Unbind returns the config without nhm's block. ACFS's palette block
// stays: it is ACFS's, and only Bind replaces it.
func Unbind(config string) (string, error) {
	return withoutBlock(config, BlockBegin, BlockEnd)
}

// quoted finds TOML's basic ("…") and literal ('…') strings.
var quoted = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)

// usesKey returns the line of config that assigns key to anything: `key =
// "prefix+a"` in a [[keys.command]], `zoom = 'prefix+a'`, or `zoom =
// ["x", "prefix+a"]` in [keys], over one line or several, any quote
// style. Each quoted value is decoded as TOML decodes it (escapes such as
// + too), then compared without spaces and ignoring case. Comments,
// table headers and multi-line strings do not count.
//
// It is a fast check with a clear message for the common spellings; the
// authority is `herdr config check`, which nhm bind runs on the result
// and which refuses any key conflict this misses.
func usesKey(config, key string) string {
	open := ""
	for _, line := range strings.Split(config, "\n") {
		inString := open != ""
		open = multiline(open, line)
		t := strings.TrimSpace(codeOf(line))
		if inString || t == "" || strings.HasPrefix(t, "[") && !strings.Contains(t, "=") {
			continue
		}
		// The value side of `name = value`; an array's later lines have
		// no name.
		if i := strings.Index(t, "="); i >= 0 && !strings.ContainsAny(t[:i], `"'`) {
			t = t[i+1:]
		}
		for _, m := range quoted.FindAllString(t, -1) {
			var v struct{ V string }
			if _, err := toml.Decode("V = "+m, &v); err != nil {
				continue
			}
			if strings.EqualFold(strings.Join(strings.Fields(v.V), ""), key) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// Bind returns the config with nhm's block in place of any earlier one,
// ACFS's palette block removed, and the block appended at the end. It
// refuses when the rest of the config already uses one of nhm's keys, or
// when the result is not valid TOML (an inline `[keys] command = [...]`
// cannot take an appended [[keys.command]]: herdr would then ignore the
// whole file).
func Bind(config string) (string, error) {
	rest, err := Unbind(config)
	if err == nil {
		rest, err = withoutBlock(rest, acfsBlockBegin, acfsBlockEnd)
	}
	if err != nil {
		return "", err
	}
	for _, k := range Keys {
		if line := usesKey(rest, k.Key); line != "" {
			return "", fmt.Errorf("ui: herdr's config already binds %s (%s); free it, then run nhm bind again", k.Key, line)
		}
	}
	rest = strings.TrimRight(rest, "\n")
	if rest != "" {
		rest += "\n\n"
	}
	next := rest + Block()
	if _, err := toml.Decode(next, new(map[string]any)); err != nil {
		return "", fmt.Errorf("ui: with nhm's keys appended, herdr's config would not parse (%v); "+
			"move its key bindings to [[keys.command]] tables, then run nhm bind again", err)
	}
	return next, nil
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
