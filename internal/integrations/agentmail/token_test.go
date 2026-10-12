package agentmail

import (
	"context"
	"testing"
)

// mismatch is Agent Mail's refusal of a stale token (am 0.3.38).
const mismatch = `{"error":{"type":"AUTHENTICATION_REQUIRED","message":"unretire_agent was refused: the supplied registration_token does not match agent 'IcyKnoll'.","recoverable":true,"data":{"agent_name":"IcyKnoll","transport":"http","policy":"token_required","reason":"token_mismatch"}}}`

func TestCheckToken(t *testing.T) {
	active := `{"id":3,"name":"IcyKnoll","retired_at":null}`
	cases := []struct {
		name      string
		whois     string
		whoisErr  bool
		unretire  string
		unretErr  bool
		want      string
		wantErr   bool
		wantCalls int
	}{
		{name: "current", whois: active, unretire: `{"status":"active","agent_name":"IcyKnoll"}`, want: TokenCurrent, wantCalls: 2},
		{name: "stale", whois: active, unretire: mismatch, unretErr: true, want: TokenStale, wantCalls: 2},
		// A retired identity is never sent unretire_agent: it would come
		// back.
		{name: "retired", whois: `{"name":"IcyKnoll","retired_at":"2026-10-12T15:30:11Z"}`, want: TokenRetired, wantCalls: 1},
		{name: "no agent", whois: `{"error":{"type":"AGENT_NOT_FOUND","message":"Agent 'IcyKnoll' not found"}}`, whoisErr: true,
			want: TokenNoAgent, wantCalls: 1},
		{name: "other refusal", whois: active, unretire: `{"error":{"type":"AUTHENTICATION_REQUIRED","data":{"reason":"token_required"}}}`,
			unretErr: true, wantErr: true, wantCalls: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []rpcCall
			srv, _ := mailServer(t, func(c rpcCall) (string, bool) {
				calls = append(calls, c)
				if c.Params.Name == "whois" {
					return tc.whois, tc.whoisErr
				}
				return tc.unretire, tc.unretErr
			})
			got, err := CheckToken(context.Background(), &Client{URL: srv.URL}, "/p", "IcyKnoll", "tok-1")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("CheckToken = %q, %v; want %q (error %v)", got, err, tc.want, tc.wantErr)
			}
			if len(calls) != tc.wantCalls {
				t.Fatalf("%d calls, want %d: %+v", len(calls), tc.wantCalls, calls)
			}
			if len(calls) == 2 {
				a := calls[1].Params.Arguments
				if calls[1].Params.Name != "unretire_agent" || a["registration_token"] != "tok-1" || a["agent_name"] != "IcyKnoll" ||
					a["project_key"] != "/p" {
					t.Fatalf("second call = %+v", calls[1])
				}
			}
		})
	}
}

func TestIsTokenMismatch(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&ToolError{Tool: "retire_agent", Message: mismatch}, true},
		{&ToolError{Tool: "retire_agent", Message: `{"error":{"type":"AUTHENTICATION_REQUIRED","data":{"reason":"token_required"}}}`}, false},
		// A JSON-RPC error has only a message.
		{&ToolError{Tool: "retire_agent", Message: "retire_agent was refused: the supplied registration_token does not match agent 'X'."}, true},
		{&ToolError{Tool: "retire_agent", Message: "Agent not found"}, false},
		{context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		if got := IsTokenMismatch(tc.err); got != tc.want {
			t.Errorf("IsTokenMismatch(%v) = %v", tc.err, got)
		}
	}
}
