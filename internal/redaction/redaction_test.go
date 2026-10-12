package redaction

import (
	"strings"
	"testing"
)

// The secrets are built from parts, so no scanner takes this file for a
// leak.
func TestRedactReplacesEachKind(t *testing.T) {
	anthropic := "sk-" + "ant-" + strings.Repeat("a1B2", 12)
	github := "gh" + "p_" + strings.Repeat("Z9y8", 9)
	aws := "AK" + "IA" + "ABCDEFGHIJKLMNOP"
	jwt := "ey" + "JhbGciOiJIUzI1NiJ9.ey" + "JzdWIiOiIxIn0.c2lnbmF0dXJl"
	for _, c := range []struct {
		in, want string
	}{
		{"key " + anthropic + " here", "key [REDACTED:ANTHROPIC_KEY] here"},
		{"token " + github, "token [REDACTED:GITHUB_TOKEN]"},
		{"id=" + aws, "id=[REDACTED:AWS_ACCESS_KEY]"},
		{"Authorization: Bearer " + strings.Repeat("x", 24), "Authorization: [REDACTED:BEARER_TOKEN]"},
		{"jwt " + jwt, "jwt [REDACTED:JWT]"},
		{"db postgres://u:" + "pw@host/db ok", "db [REDACTED:DATABASE_URL] ok"},
		{"password = hunter2hunter2", "[REDACTED:PASSWORD]"},
	} {
		got, n := Redact(c.in)
		if got != c.want || n != 1 {
			t.Errorf("Redact(%q) = %q, %d; want %q, 1", c.in, got, n, c.want)
		}
	}
}

// nhm's strengthening: the key's body goes too, also when the block was
// cut off before its END line.
func TestRedactWholePrivateKeyBlock(t *testing.T) {
	begin, end := "-----BEGIN RSA PRIVATE "+"KEY-----", "-----END RSA PRIVATE "+"KEY-----"
	body := "MIIEowIBAAKCAQEA7bq3\nQmFzZTY0Ym9keQ==\n"
	got, n := Redact("before\n" + begin + "\n" + body + end + "\nafter")
	if got != "before\n[REDACTED:PRIVATE_KEY]\nafter" || n != 1 {
		t.Fatalf("whole block = %q, %d", got, n)
	}
	got, _ = Redact("key: " + begin + "\n" + body)
	if strings.Contains(got, "MIIEow") || strings.Contains(got, "Ym9keQ") || !strings.Contains(got, "[REDACTED:PRIVATE_KEY]") {
		t.Fatalf("cut-off block = %q", got)
	}
	got, _ = Redact(begin + " QmFzZTY0 " + end + ". The rest stays.")
	if got != "[REDACTED:PRIVATE_KEY]. The rest stays." {
		t.Fatalf("one-line block = %q", got)
	}
}

// Review of fd98368 (acfs-9hkh.65): the PEM patterns run first, so a key
// body that holds what reads as another secret goes as one block, also
// when the block was cut off.
func TestPrivateKeyBeforeOtherPatterns(t *testing.T) {
	begin, end := "-----BEGIN RSA PRIVATE "+"KEY-----", "-----END RSA PRIVATE "+"KEY-----"
	body := "MIIEow" + "AK" + "IA" + "ABCDEFGHIJKLMNOP" + "Qm9keQ==\nZmlu\n"
	for _, in := range []string{begin + "\n" + body + end + "\nafter", begin + "\n" + body} {
		got, n := Redact(in)
		if strings.Contains(got, "AWS_ACCESS_KEY") || strings.Contains(got, "Zmlu") || strings.Contains(got, "Qm9k") || n != 1 {
			t.Errorf("Redact(%q) = %q, %d", in, got, n)
		}
	}
}

func TestFindSpans(t *testing.T) {
	aws := "AK" + "IA" + "ABCDEFGHIJKLMNOP"
	s := "a " + aws + " b password = hunter2hunter2"
	spans, n := Find(s)
	if n != 2 || len(spans) != 2 || s[spans[0].Start:spans[0].End] != aws || spans[0].Category != CategoryAWSAccessKey ||
		spans[1].Category != CategoryPassword || spans[1].End != len(s) {
		t.Fatalf("spans = %+v, %d", spans, n)
	}
	// A later, wider match takes in an earlier one: one span, both counted.
	s = "password=" + aws
	spans, n = Find(s)
	if n != 2 || len(spans) != 1 || spans[0].Start != 0 || spans[0].End != len(s) || spans[0].Category != CategoryPassword {
		t.Fatalf("merged spans = %+v, %d", spans, n)
	}
	if got, _ := Redact(s); got != Placeholder(CategoryPassword) {
		t.Fatalf("merged redaction = %q", got)
	}
}

func TestRedactLeavesPlainTextAlone(t *testing.T) {
	for _, s := range []string{"", "Run the login tests after an auth change.", "the token bucket refills", "sk-short"} {
		if got, n := Redact(s); got != s || n != 0 {
			t.Errorf("Redact(%q) = %q, %d", s, got, n)
		}
	}
	// A placeholder is stable: a second pass finds nothing.
	once, _ := Redact("api_key=" + strings.Repeat("k", 20))
	if twice, n := Redact(once); twice != once || n != 0 {
		t.Errorf("second pass = %q, %d", twice, n)
	}
}
