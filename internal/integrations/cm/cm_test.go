package cm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fake records the args and answers with out.
func fake(out string, err error, got *[][]string) Runner {
	return func(_ context.Context, args ...string) ([]byte, error) {
		*got = append(*got, args)
		return []byte(out), err
	}
}

const contextAnswer = `{"success":true,"command":"context","data":{"task":"fix login",
"relevantBullets":[{"id":"b-1","category":"testing","content":"Run the login tests","isNegative":false,"finalScore":2.3}],
"antiPatterns":[{"id":"b-2","category":"auth","content":"Never skip the CSRF check","isNegative":true}]}}`

func TestContextParsesRulesAndAntiPatterns(t *testing.T) {
	var got [][]string
	c := Client{Run: fake(contextAnswer, nil, &got)}
	res, err := c.Context(context.Background(), "fix login", "/p")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rules) != 1 || res.Rules[0].ID != "b-1" || res.Rules[0].Content != "Run the login tests" {
		t.Fatalf("rules = %+v", res.Rules)
	}
	if len(res.AntiPatterns) != 1 || !res.AntiPatterns[0].Negative {
		t.Fatalf("anti = %+v", res.AntiPatterns)
	}
	want := []string{"context", "fix login", "--json", "--workspace", "/p"}
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("args = %q", got[0])
	}
}

func TestFailedEnvelopeIsAnError(t *testing.T) {
	var got [][]string
	c := Client{Run: fake(`{"success":false,"error":{"code":"X","message":"boom"}}`, nil, &got)}
	if _, err := c.Context(context.Background(), "q", ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	c = Client{Run: fake(`not json`, nil, &got)}
	if err := c.OutcomeApply(context.Background(), ""); err == nil {
		t.Fatal("no error for a non-JSON answer")
	}
	c = Client{Run: fake("", ErrNotInstalled, &got)}
	if err := c.Mark(context.Background(), "b-1", true, ""); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMarkOutcomeAndApplyArgs(t *testing.T) {
	var got [][]string
	ok := `{"success":true,"data":{}}`
	c := Client{Run: fake(ok, nil, &got)}
	ctx := context.Background()
	if err := c.Mark(ctx, "b-1", false, "/s.jsonl"); err != nil {
		t.Fatal(err)
	}
	if err := c.Outcome(ctx, "success", []string{"b-1", "b-2"}, "/s.jsonl", "implicit: bead x"); err != nil {
		t.Fatal(err)
	}
	if err := c.OutcomeApply(ctx, "/s.jsonl"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"mark", "b-1", "--harmful", "--json", "--session", "/s.jsonl"},
		{"outcome", "success", "b-1,b-2", "--json", "--session", "/s.jsonl", "--text", "implicit: bead x"},
		{"outcome-apply", "--json", "--session", "/s.jsonl"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args = %q", got)
	}
}

func TestExecReportsAMissingBinary(t *testing.T) {
	run := Exec("/nonexistent/cm-binary")
	if _, err := run(context.Background(), "context", "q"); err == nil {
		t.Fatal("no error")
	}
	run = Exec("nhm-test-no-such-cm")
	if _, err := run(context.Background(), "context", "q"); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}
