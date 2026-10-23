package quota

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// caamTimeout bounds `caam status` for the verbs; caamRecordTimeout for
// the statusLine command, which runs at every refresh of Claude's status
// line (caam takes about 50 ms). A record without the profile still has
// the fingerprint, which match then compares.
const (
	caamTimeout       = 2 * time.Second
	caamRecordTimeout = 500 * time.Millisecond
)

// Credential names the account a pool's records are of (plan-v8 2.9): a
// caam switch or a new login changes it, and an observation of another
// credential is unknown. It holds names only, never a secret: caam's
// active profile, and a fingerprint of the account in the kind's auth
// file.
type Credential struct {
	// Profile is caam's active profile for the kind (`caam status --json`,
	// tools[].active_profile).
	Profile string `json:"profile,omitempty"`
	// Fingerprint is the first 12 hex digits of a SHA-256 over the kind
	// and the account's id in the auth file (Claude's oauthAccount in
	// .claude.json, Codex's tokens.account_id in auth.json), or over the
	// auth file when it names no account. It tells two accounts apart and
	// shows nothing of either.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// match compares two credentials: by caam profile when both name one, else
// by fingerprint when both have one. known is false when they share
// neither field, so nhm cannot tell.
func match(a, b *Credential) (same, known bool) {
	switch {
	case a == nil || b == nil:
		return false, false
	case a.Profile != "" && b.Profile != "":
		return a.Profile == b.Profile, true
	case a.Fingerprint != "" && b.Fingerprint != "":
		return a.Fingerprint == b.Fingerprint, true
	}
	return false, false
}

// ofActive reports which records, given newest first by their
// credentials, are of the active credential: a record that names it, or a
// record that names no credential (nhm before nhm-kyt, ACFS's
// record-claude, a Codex log without its session_meta) when no newer
// record names another credential. other is the newest credential that is
// not the active one, nil when no record was left out for one. With no
// active credential, every record counts: nhm has nothing to compare.
func ofActive(active *Credential, creds []*Credential) (kept []bool, other *Credential) {
	kept = make([]bool, len(creds))
	for i := range creds {
		same, known := match(active, creds[i])
		switch {
		case known && same:
			kept[i] = true
		case known:
			if other == nil {
				other = creds[i]
			}
		default:
			kept[i] = other == nil
		}
	}
	return kept, other
}

// ActiveCredential is the credential the kind uses now, nil when nhm
// cannot name it. It is the statusLine command's: caam has
// caamRecordTimeout.
func ActiveCredential(env Env, kind string) *Credential {
	return credentialOf(env, kind, caamProfiles(env.Caam, kind, caamRecordTimeout))
}

func credentialOf(env Env, kind string, profiles map[string]string) *Credential {
	c := &Credential{Profile: profiles[kind]}
	switch kind {
	case "claude":
		c.Fingerprint = claudeFingerprint(env)
	case "codex":
		c.Fingerprint = codexFingerprint(env)
	}
	if c.Profile == "" && c.Fingerprint == "" {
		return nil
	}
	return c
}

// caamProfiles returns caam's active profile per tool, of one tool when
// tool is not "". It reads only tools[].tool and .active_profile: caam's
// identity block holds the account's email and id. No caam, a failing
// one or one slower than limit gives none.
func caamProfiles(bin, tool string, limit time.Duration) map[string]string {
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	args := []string{"status", "--json"}
	if tool != "" {
		args = []string{"status", tool, "--json"}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	// A child of caam that holds stdout open holds the call at most
	// WaitDelay more (nhm-fty).
	cmd.WaitDelay = min(time.Second, limit)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var st struct {
		Tools []struct {
			Tool          string `json:"tool"`
			ActiveProfile string `json:"active_profile"`
		} `json:"tools"`
	}
	if json.Unmarshal(out, &st) != nil {
		return nil
	}
	profiles := map[string]string{}
	for _, t := range st.Tools {
		if t.Tool != "" && t.ActiveProfile != "" {
			profiles[t.Tool] = t.ActiveProfile
		}
	}
	return profiles
}

// fingerprint is a short one-way name for an account id or an auth file.
func fingerprint(kind, what string, value []byte) string {
	h := sha256.New()
	h.Write([]byte("nhm quota credential\x00" + kind + "\x00" + what + "\x00"))
	h.Write(value)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

func accountFingerprint(kind, id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return ""
	}
	return fingerprint(kind, "account", []byte(id))
}

// claudeFingerprint names Claude's account by .claude.json's
// oauthAccount.accountUuid, which a token refresh keeps; Claude Code
// rotates the tokens in .credentials.json every few hours of use. Without
// an account there (an API key), the credentials file names it. A
// .claude.json that does not parse (Claude Code writes it often) names
// nothing: the file's fingerprint would never match the account's.
func claudeFingerprint(env Env) string {
	data, err := os.ReadFile(env.ClaudeState)
	switch {
	case err == nil:
		var st struct {
			OAuthAccount json.RawMessage `json:"oauthAccount"`
		}
		if json.Unmarshal(data, &st) != nil {
			return ""
		}
		var acct struct {
			AccountUUID string `json:"accountUuid"`
		}
		var legacy string
		switch {
		case json.Unmarshal(st.OAuthAccount, &acct) == nil && acct.AccountUUID != "":
			return accountFingerprint("claude", acct.AccountUUID)
		case json.Unmarshal(st.OAuthAccount, &legacy) == nil && legacy != "":
			return accountFingerprint("claude", legacy)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return ""
	}
	if env.ClaudeDir == "" {
		return ""
	}
	return fileFingerprint("claude", filepath.Join(env.ClaudeDir, ".credentials.json"))
}

// codexFingerprint names Codex's account by auth.json's tokens.account_id,
// the id each session log's session_meta names as creator_account_id;
// without one (an API key), by the file. An auth.json that does not parse
// names nothing.
func codexFingerprint(env Env) string {
	if env.CodexHome == "" {
		return ""
	}
	path := filepath.Join(env.CodexHome, "auth.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var auth struct {
		Tokens *struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil {
		return ""
	}
	if auth.Tokens != nil && auth.Tokens.AccountID != "" {
		return accountFingerprint("codex", auth.Tokens.AccountID)
	}
	return fileFingerprint("codex", path)
}

func fileFingerprint(kind, path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return ""
	}
	return fingerprint(kind, "file", data)
}
