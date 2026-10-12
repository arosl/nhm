package guards

// Codex runs a hook only when config.toml holds its current trusted_hash
// under [hooks.state."<abs hooks.json path>:<event label>:<group>:<handler>"].
// The hash below is Codex's (codex-rs rust-v0.162.1:
// hooks/src/engine/discovery.rs hook_hash, events/common.rs
// matcher_pattern_for_event, config/src/fingerprint.rs), as ACFS's exp.6
// reproduced it on every entry: SHA-256 of the compact, key-sorted JSON of
// {event_name, matcher?, hooks: [normalised handler]}.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

var eventLabels = map[string]string{
	"PreToolUse": "pre_tool_use", "PermissionRequest": "permission_request", "PostToolUse": "post_tool_use",
	"PreCompact": "pre_compact", "PostCompact": "post_compact", "SessionStart": "session_start",
	"SessionEnd": "session_end", "UserPromptSubmit": "user_prompt_submit", "SubagentStart": "subagent_start",
	"SubagentStop": "subagent_stop", "Stop": "stop", "Interrupt": "interrupt",
}

// Codex drops these events' matcher before hashing (and matching).
var noMatcher = map[string]bool{"UserPromptSubmit": true, "Stop": true, "Interrupt": true}

// These events' timeout defaults to 1 s and is clamped to 1..3.
var shortTimeout = map[string]bool{"SessionEnd": true, "Interrupt": true}

var contextEvents = map[string]bool{"PreToolUse": true, "PostToolUse": true, "SessionStart": true,
	"UserPromptSubmit": true, "SubagentStart": true}

const defaultContextLimit = 2500

// codexHandler is a hooks.json handler with the fields the hash reads.
type codexHandler struct {
	Type                   string   `json:"type"`
	Command                string   `json:"command"`
	Timeout                *float64 `json:"timeout"`
	Async                  bool     `json:"async"`
	StatusMessage          *string  `json:"statusMessage"`
	AdditionalContextLimit *float64 `json:"additionalContextLimit"`
}

type codexGroup struct {
	Matcher *string        `json:"matcher"`
	Hooks   []codexHandler `json:"hooks"`
}

// CodexKey is the trust key of a handler.
func CodexKey(hooksPath, event string, group, handler int) string {
	return hooksPath + ":" + eventLabels[event] + ":" + itoa(group) + ":" + itoa(handler)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// CodexHash is the trusted_hash Codex expects for a command handler.
func CodexHash(event string, matcher *string, h codexHandler) (string, error) {
	var timeout float64
	switch {
	case shortTimeout[event]:
		timeout = 1
		if h.Timeout != nil {
			timeout = *h.Timeout
		}
		timeout = min(max(timeout, 1), 3)
	default:
		timeout = 600
		if h.Timeout != nil {
			timeout = *h.Timeout
		}
		timeout = max(timeout, 1)
	}
	norm := map[string]any{"type": "command", "command": h.Command, "timeout": int64(timeout), "async": h.Async}
	if h.StatusMessage != nil {
		norm["statusMessage"] = *h.StatusMessage
	}
	if contextEvents[event] && h.AdditionalContextLimit != nil && *h.AdditionalContextLimit != defaultContextLimit {
		norm["additionalContextLimit"] = int64(*h.AdditionalContextLimit)
	}
	identity := map[string]any{"event_name": eventLabels[event], "hooks": []any{norm}}
	if matcher != nil && !noMatcher[event] {
		identity["matcher"] = *matcher
	}
	// encoding/json sorts map keys; Python's json.dumps(ensure_ascii=False)
	// does not escape HTML characters, so neither may this.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(identity); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n"))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// codexTrust reads the trusted hashes and the trusted projects of a
// config.toml.
func codexTrust(path string) (hashes map[string]string, trustedProjects map[string]bool, err error) {
	var cfg struct {
		Hooks struct {
			State map[string]struct {
				TrustedHash string `toml:"trusted_hash"`
			} `toml:"state"`
		} `toml:"hooks"`
		Projects map[string]struct {
			TrustLevel string `toml:"trust_level"`
		} `toml:"projects"`
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, map[string]bool{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return nil, nil, err
	}
	hashes, trustedProjects = map[string]string{}, map[string]bool{}
	for k, v := range cfg.Hooks.State {
		hashes[k] = v.TrustedHash
	}
	for p, v := range cfg.Projects {
		if v.TrustLevel == "trusted" {
			trustedProjects[p] = true
		}
	}
	return hashes, trustedProjects, nil
}

func readCodexHooks(path string) (map[string][]codexGroup, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var f struct {
		Hooks map[string][]codexGroup `json:"hooks"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, false, err
	}
	return f.Hooks, true, nil
}

func checkCodex(env Env) []Result {
	cfgPath := filepath.Join(env.CodexHome, "config.toml")
	hashes, trustedProjects, cfgErr := codexTrust(cfgPath)
	files := []string{filepath.Join(env.CodexHome, "hooks.json")}
	if env.ProjectRoot != "" && trustedProjects[env.ProjectRoot] {
		files = append(files, filepath.Join(env.ProjectRoot, ".codex", "hooks.json"))
	}
	return each("codex", func(g string) Result {
		if cfgErr != nil {
			return Result{State: Missing, Detail: cfgPath + ": " + cfgErr.Error()}
		}
		var untrusted []string
		for _, path := range files {
			hooks, ok, err := readCodexHooks(path)
			if err != nil {
				return Result{State: Missing, Detail: path + ": " + err.Error()}
			}
			if !ok {
				continue
			}
			plain := hooksFile{Hooks: map[string][]hookGroup{}}
			for ev, groups := range hooks {
				for _, grp := range groups {
					hg := hookGroup{Matcher: grp.Matcher}
					for _, h := range grp.Hooks {
						hg.Hooks = append(hg.Hooks, hookHandler{Type: h.Type, Command: h.Command})
					}
					plain.Hooks[ev] = append(plain.Hooks[ev], hg)
				}
			}
			event, gi, hi, ok := holds(plain, g)
			if !ok {
				continue
			}
			key := CodexKey(path, event, gi, hi)
			want, err := CodexHash(event, hooks[event][gi].Matcher, hooks[event][gi].Hooks[hi])
			if err != nil {
				return Result{State: Missing, Detail: err.Error()}
			}
			switch hashes[key] {
			case want:
				return Result{State: Configured, Sources: []string{key}}
			case "":
				untrusted = append(untrusted, key+" is not trusted")
			default:
				untrusted = append(untrusted, key+" changed since it was trusted")
			}
		}
		if len(untrusted) > 0 {
			return Result{State: Untrusted, Detail: strings.Join(untrusted, "; ") + "; an untrusted Codex hook does not run",
				Fix: "record trust through Codex's own app-server (hooks/list, then config/batchWrite), as ACFS's install does (F.25)"}
		}
		return Result{State: Missing, Detail: "no Codex hooks file holds it", Fix: fixFor("codex", g)}
	})
}
