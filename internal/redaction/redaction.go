// Derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/redaction/patterns.go at commit 9d5ed977; see LICENSE in this
// directory and PROVENANCE.md. Local changes: only the built-in pattern
// table, its categories and the literal prefilter are kept; Redact
// replaces each match with [REDACTED:<CATEGORY>], as ACFS's Bash
// (herdr_agents_redact) does, without ntm's hash suffix, allowlist,
// extra patterns and line info; a private key's whole PEM block is
// redacted, not only its header line, and the PEM patterns run first (a
// deliberate strengthening); Find returns the secrets as spans, and
// Redact is built on it.

// Package redaction replaces secrets in text that nhm puts into a prompt
// from elsewhere: CM rules and CASS hits from other sessions.
package redaction

import (
	"regexp"
	"sort"
	"strings"
)

// Category is the kind of secret found.
type Category string

// Categories, as ntm names them.
const (
	CategoryOpenAIKey     Category = "OPENAI_KEY"
	CategoryAnthropicKey  Category = "ANTHROPIC_KEY"
	CategoryGitHubToken   Category = "GITHUB_TOKEN"
	CategoryAWSAccessKey  Category = "AWS_ACCESS_KEY"
	CategoryAWSSecretKey  Category = "AWS_SECRET_KEY"
	CategoryJWT           Category = "JWT"
	CategoryGoogleAPIKey  Category = "GOOGLE_API_KEY"
	CategoryPrivateKey    Category = "PRIVATE_KEY"
	CategoryDatabaseURL   Category = "DATABASE_URL"
	CategoryPassword      Category = "PASSWORD"
	CategoryGenericAPIKey Category = "GENERIC_API_KEY"
	CategoryGenericSecret Category = "GENERIC_SECRET"
	CategoryBearerToken   Category = "BEARER_TOKEN"
)

const (
	openAIPrefixPattern     = "s" + "k\\-"
	openAIProjPrefixPattern = openAIPrefixPattern + "proj\\-"
	openAIPrefixLiteral     = "s" + "k-"
	openAIProjPrefixLiteral = openAIPrefixLiteral + "proj-"
	openAIMarker            = "T3Blbk" + "FJ"
)

// literals is a prefilter: a pattern runs only when every group has one
// of its needles in the text.
type literals struct {
	anyOf      []string
	ignoreCase bool
}

type pattern struct {
	category Category
	re       *regexp.Regexp
	priority int
	need     []literals
}

// patterns is ntm's built-in table, most specific first.
var patterns = compile([]struct {
	category Category
	expr     string
	priority int
	need     []literals
}{
	{CategoryOpenAIKey, openAIPrefixPattern + `[a-zA-Z0-9]{10,}` + openAIMarker + `[a-zA-Z0-9]{10,}`, 100,
		[]literals{{anyOf: []string{openAIPrefixLiteral}}, {anyOf: []string{openAIMarker}}}},
	{CategoryOpenAIKey, openAIProjPrefixPattern + `[a-zA-Z0-9_-]{40,}`, 100, []literals{{anyOf: []string{openAIProjPrefixLiteral}}}},
	{CategoryOpenAIKey, openAIPrefixPattern + `[a-zA-Z0-9]{48}`, 95, []literals{{anyOf: []string{openAIPrefixLiteral}}}},
	{CategoryAnthropicKey, `sk\-ant\-[a-zA-Z0-9_-]{40,}`, 100, []literals{{anyOf: []string{"sk-ant-"}}}},
	{CategoryGitHubToken, `gh[pousr]_[a-zA-Z0-9]{30,}`, 100, []literals{{anyOf: []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"}}}},
	{CategoryGitHubToken, `github_pat_[a-zA-Z0-9]{20,}_[a-zA-Z0-9]{40,}`, 100, []literals{{anyOf: []string{"github_pat_"}}}},
	{CategoryGoogleAPIKey, `AIza[a-zA-Z0-9_-]{35}`, 100, []literals{{anyOf: []string{"AIza"}}}},
	{CategoryAWSAccessKey, `AKIA[0-9A-Z]{16}`, 90, []literals{{anyOf: []string{"AKIA"}}}},
	{CategoryAWSAccessKey, `ASIA[0-9A-Z]{16}`, 90, []literals{{anyOf: []string{"ASIA"}}}},
	{CategoryAWSSecretKey, `(?i)(aws_secret|secret_access_key|secret_key)\s*[=:]\s*["']?[a-zA-Z0-9/+=]{40}["']?`, 90,
		[]literals{{anyOf: []string{"aws_secret", "secret_access_key", "secret_key"}, ignoreCase: true}}},
	{CategoryJWT, `eyJ[a-zA-Z0-9_-]*\.eyJ[a-zA-Z0-9_-]*\.[a-zA-Z0-9_-]+`, 85, []literals{{anyOf: []string{"eyJ"}}}},
	{CategoryBearerToken, `(?i)bearer\s+[a-zA-Z0-9._-]{20,}`, 80, []literals{{anyOf: []string{"bearer"}, ignoreCase: true}}},
	// nhm's strengthening: ntm redacts only the PEM header line, so the
	// key's body reached the prompt. The whole block goes, and a block cut
	// off before its END line (a hit truncated to its budget) goes to the
	// end of its base64 body. Both run before every other pattern: a key
	// body can hold what reads as another secret (AKIA…, ghp_…), and a
	// match there would cut the block in two and leave the rest of the
	// body in the prompt.
	{CategoryPrivateKey, `-----BEGIN\s+([A-Z]+\s+)?PRIVATE KEY-----[\s\S]*?-----END\s+([A-Z]+\s+)?PRIVATE KEY-----`, 120,
		[]literals{{anyOf: []string{"private key"}, ignoreCase: true}}},
	{CategoryPrivateKey, `-----BEGIN\s+([A-Z]+\s+)?PRIVATE KEY-----[A-Za-z0-9+/=:,\s\\-]*`, 119,
		[]literals{{anyOf: []string{"private key"}, ignoreCase: true}}},
	{CategoryDatabaseURL, `(?i)(postgres|mysql|mongodb|redis)://[^:]+:[^@]+@[^\s]+`, 85,
		[]literals{{anyOf: []string{"postgres://", "mysql://", "mongodb://", "redis://"}, ignoreCase: true}}},
	{CategoryPassword, `(?i)(password|passwd|pwd)\s*[=:]\s*["']?[^\s"']{8,}["']?`, 50,
		[]literals{{anyOf: []string{"password", "passwd", "pwd"}, ignoreCase: true}}},
	{CategoryGenericAPIKey, `(?i)([a-z_]*api[_]?key)\s*[=:]\s*["']?[a-zA-Z0-9_-]{16,}["']?`, 40,
		[]literals{{anyOf: []string{"api_key", "apikey"}, ignoreCase: true}}},
	{CategoryGenericSecret, `(?i)(secret|private[_]?key|token)\s*[=:]\s*["']?[a-zA-Z0-9/+=_-]{16,}["']?`, 30,
		[]literals{{anyOf: []string{"secret", "private_key", "privatekey", "token"}, ignoreCase: true}}},
})

func compile(defs []struct {
	category Category
	expr     string
	priority int
	need     []literals
}) []pattern {
	out := make([]pattern, 0, len(defs))
	for _, d := range defs {
		need := make([]literals, 0, len(d.need))
		for _, l := range d.need {
			if l.ignoreCase {
				lower := make([]string, len(l.anyOf))
				for i, s := range l.anyOf {
					lower[i] = strings.ToLower(s)
				}
				l.anyOf = lower
			}
			need = append(need, l)
		}
		out = append(out, pattern{category: d.category, re: regexp.MustCompile(d.expr), priority: d.priority, need: need})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].priority > out[j].priority })
	return out
}

func (p pattern) mayMatch(s, lower string) bool {
	for _, l := range p.need {
		hay := s
		if l.ignoreCase {
			hay = lower
		}
		found := false
		for _, needle := range l.anyOf {
			if strings.Contains(hay, needle) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Span is one secret that Find found: s[Start:End], of Category.
type Span struct {
	Start, End int
	Category   Category
}

// Placeholder is what Redact puts in place of a secret.
func Placeholder(c Category) string { return "[REDACTED:" + string(c) + "]" }

// masked is the byte a found secret becomes for the later patterns, as a
// placeholder would be: no pattern's own literal, and not a space.
const masked = 0

// Find returns the secrets in s and the number of matches. The most
// specific pattern runs first, and a later, generic one sees only masked
// bytes where an earlier one matched; a later match that takes in an
// earlier one replaces it. The spans are sorted and do not overlap.
// Callers that check text in another form (enrich folds it first) map the
// spans back to their own text.
func Find(s string) ([]Span, int) {
	buf := []byte(s)
	var spans []Span
	n := 0
	for _, p := range patterns {
		str := string(buf)
		if !p.mayMatch(str, strings.ToLower(str)) {
			continue
		}
		for _, m := range p.re.FindAllStringIndex(str, -1) {
			n++
			sp := Span{Start: m[0], End: m[1], Category: p.category}
			kept := spans[:0]
			for _, old := range spans {
				if old.End <= sp.Start || old.Start >= sp.End {
					kept = append(kept, old)
					continue
				}
				sp.Start, sp.End = min(sp.Start, old.Start), max(sp.End, old.End)
			}
			spans = append(kept, sp)
			for i := sp.Start; i < sp.End; i++ {
				buf[i] = masked
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return spans, n
}

// Redact replaces every secret in s with [REDACTED:<CATEGORY>] and
// returns the text and the number of replacements (Find's spans and
// count).
func Redact(s string) (string, int) {
	spans, n := Find(s)
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(s[last:sp.Start])
		b.WriteString(Placeholder(sp.Category))
		last = sp.End
	}
	b.WriteString(s[last:])
	return b.String(), n
}
