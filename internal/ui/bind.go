package ui

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Key is one key `nhm bind` writes: a herdr key and the plugin action it
// runs. Name is the key's name in [keys] of nhm's config. An empty Key
// gives the action no direct key.
type Key struct {
	Name   string
	Key    string
	Action string
	What   string
}

// Keys are nhm's default keys (the operator, 2026-10-12): no F keys;
// prefix+a, prefix+d and prefix+m are free in herdr 0.9.3's defaults
// (prefix+s is herdr's settings). [keys] in nhm's config changes them
// (LoadKeys).
var Keys = []Key{
	{Name: "palette", Key: "prefix+a", Action: "nhm.palette", What: "the command palette"},
	{Name: "board", Key: "prefix+d", Action: "nhm.board", What: "the agent board"},
	{Name: "menu", Key: "prefix+m", Action: "nhm.menu", What: "the nhm menu"},
}

// LoadKeys returns nhm's keys as the [keys] table of nhm's config file at
// path sets them (plan-v4 section 2, the operator's D10):
//
//	[keys]
//	palette = "prefix+a"   # the defaults
//	board   = "prefix+d"
//	menu    = "prefix+m"
//
// A name the table leaves out keeps its default. An empty value gives
// that surface no direct key; the menu still opens it. A missing file is
// the defaults. Other tables of the file belong to other parts of nhm.
// herdr's `config check`, which nhm bind runs, is the authority on a
// key's syntax; LoadKeys refuses only an unknown name, a control
// character and one key for two surfaces.
func LoadKeys(path string) ([]Key, error) {
	keys := slices.Clone(Keys)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return keys, nil
	}
	if err != nil {
		return nil, err
	}
	var c struct {
		Keys map[string]string `toml:"keys"`
	}
	if _, err := toml.Decode(string(data), &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, name := range slices.Sorted(maps.Keys(c.Keys)) {
		i := slices.IndexFunc(keys, func(k Key) bool { return k.Name == name })
		if i < 0 {
			return nil, fmt.Errorf("%s: [keys] has no %q; its names are palette, board and menu", path, name)
		}
		v := strings.TrimSpace(c.Keys[name])
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("%s: [keys] %s = %q is not a key", path, name, v)
		}
		keys[i].Key = v
	}
	for i, a := range keys {
		for _, b := range keys[i+1:] {
			if a.Key != "" && sameKey(a.Key, b.Key) {
				return nil, fmt.Errorf("%s: [keys] %s and %s are both %s; give each its own key", path, a.Name, b.Name, a.Key)
			}
		}
	}
	return keys, nil
}

// Describe names the keys for a message: "prefix+a palette, prefix+d
// board, menu without a key".
func Describe(keys []Key) string {
	var parts []string
	for _, k := range keys {
		if k.Key == "" {
			parts = append(parts, k.Name+" without a key")
		} else {
			parts = append(parts, k.Key+" "+k.Name)
		}
	}
	return strings.Join(parts, ", ")
}

// The markers of the block `nhm bind` manages, and of the block ACFS's
// `acfs palette bind` wrote before nhm took over the palette.
const (
	BlockBegin     = "# >>> nhm >>>"
	BlockEnd       = "# <<< nhm <<<"
	acfsBlockBegin = "# >>> acfs palette >>>"
	acfsBlockEnd   = "# <<< acfs palette <<<"
)

// optOutBlock is what `nhm bind --unbind` leaves: nhm's markers with no
// keys between them, as ACFS's `acfs palette unbind` leaves its own. nhm
// bind without --force binds nothing while either is in herdr's config
// (ACFS acfs-uetm: acfs update runs nhm bind each time).
const optOutBlock = BlockBegin + " unbound by `nhm bind --unbind`; `nhm bind --force` binds it again\n" + BlockEnd + "\n"

// optOutTag follows the begin marker of an opt-out, nhm's or ACFS's.
const optOutTag = " unbound by "

// ErrOptedOut is Bind's refusal of a config that records an opt-out.
var ErrOptedOut = errors.New("ui: herdr's config opts out of nhm's keys")

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

// Block is the text `nhm bind` writes for keys. A key without a herdr key
// gets a comment, not a binding.
func Block(keys []Key) string {
	var b strings.Builder
	b.WriteString(BlockBegin + "\n")
	b.WriteString("# Written by `nhm bind` from [keys] in nhm's config.toml; `nhm bind --unbind` removes it. Edits inside are lost.\n")
	for _, k := range keys {
		if k.Key == "" {
			fmt.Fprintf(&b, "\n# %s (%s): no key ([keys] %s = \"\")\n", k.What, k.Action, k.Name)
			continue
		}
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
// outside a string. A multi-line string that closes on the line is
// skipped like any string; a line that leaves one open is kept whole.
func codeOf(line string) string {
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '#':
			return line[:i]
		case '"', '\'':
			if d := line[i:min(i+3, len(line))]; d == `"""` || d == `'''` {
				j := closeIndex(line[i+3:], d)
				if j < 0 {
					return line
				}
				i += 3 + j - 1
				continue
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

// isMarker reports whether line is the begin marker, alone or followed by
// a comment: ACFS writes "# >>> acfs palette >>> managed by `acfs palette
// bind`; …" and, after `acfs palette unbind`, "… unbound by …".
func isMarker(line, marker string) bool {
	t := strings.TrimSpace(line)
	return t == marker || strings.HasPrefix(t, marker+" ")
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
		case !inside && isMarker(line, begin):
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

// Unbind returns the config without nhm's block, its keys or its opt-out.
// ACFS's palette block stays: it is ACFS's, and only Bind replaces it.
func Unbind(config string) (string, error) {
	return withoutBlock(config, BlockBegin, BlockEnd)
}

// OptOut returns the config without nhm's keys and with nhm's opt-out
// block at the end, which `nhm bind --unbind` writes. ACFS's palette block
// stays, as in Unbind.
func OptOut(config string) (string, error) {
	rest, err := Unbind(config)
	if err != nil {
		return "", err
	}
	return withBlock(rest, optOutBlock), nil
}

// OptOutBy names the command that recorded an opt-out in the config:
// "nhm bind --unbind" for nhm's opt-out block, "acfs palette unbind" for
// ACFS's unbound palette block, "" for none. A marker counts only at the
// top level, never inside a multi-line string.
func OptOutBy(config string) string {
	open := ""
	for _, line := range strings.Split(config, "\n") {
		inString := open != ""
		open = multiline(open, line)
		if inString {
			continue
		}
		switch t := strings.TrimSpace(line); {
		case strings.HasPrefix(t, BlockBegin+optOutTag):
			return "nhm bind --unbind"
		case strings.HasPrefix(t, acfsBlockBegin+optOutTag):
			return "acfs palette unbind"
		}
	}
	return ""
}

// withBlock appends block to config after one blank line.
func withBlock(config, block string) string {
	config = strings.TrimRight(config, "\n")
	if config != "" {
		config += "\n\n"
	}
	return config + block
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
			if sameKey(v.V, key) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// sameKey compares two keys without spaces and ignoring case. herdr's own
// match is stricter (it refuses "Prefix+A"), so a key this calls the same
// is never missed.
func sameKey(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), ""), strings.Join(strings.Fields(b), ""))
}

// Bind is BindKeys with the default keys, without force. nhm bind uses
// BindKeys with the keys of nhm's config.
func Bind(config string) (string, error) {
	return BindKeys(config, Keys, false)
}

// BindKeys returns the config with nhm's block for keys in place of any
// earlier one, ACFS's palette block removed, and the block appended at
// the end. It refuses (ErrOptedOut) a config that records an opt-out
// (OptOutBy) unless force is set; with force, the opt-out goes too. It
// refuses when the rest of the config already uses one of the keys, or
// when the result is not valid TOML (an inline `[keys] command = [...]`
// cannot take an appended [[keys.command]]: herdr would then ignore the
// whole file).
func BindKeys(config string, keys []Key, force bool) (string, error) {
	if by := OptOutBy(config); by != "" && !force {
		return "", fmt.Errorf("%w (%s)", ErrOptedOut, by)
	}
	rest, err := Unbind(config)
	if err == nil {
		rest, err = withoutBlock(rest, acfsBlockBegin, acfsBlockEnd)
	}
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if k.Key == "" {
			continue
		}
		if line := usesKey(rest, k.Key); line != "" {
			return "", fmt.Errorf("ui: herdr's config already binds %s (%s); free it or give %s another key in [keys] of nhm's config, then run nhm bind again", k.Key, line, k.Name)
		}
	}
	next := withBlock(rest, Block(keys))
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
