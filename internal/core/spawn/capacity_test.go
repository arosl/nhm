package spawn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/integrations/quota"
)

// The two ways out that a capacity refusal names (nhm-1pq).
const (
	wayOutFlag   = "--ignore-capacity for this spawn"
	wayOutConfig = "capacity = [] under [providers] in ~/.config/nhm/config.toml"
)

func capacityOf(status string, reasons ...string) func(context.Context) (Capacity, error) {
	return func(context.Context) (Capacity, error) { return Capacity{Status: status, Reasons: reasons}, nil }
}

// A red capacity refuses, and so does an unknown one in the robot
// profile; each refusal names --ignore-capacity and the config opt-out.
// --ignore-capacity admits both with a warning, in either profile. It
// covers the capacity only: --force does not lift a capacity refusal, and
// --ignore-capacity does not lift a quota one.
func TestCapacityAdmission(t *testing.T) {
	now := time.Now()
	at := now.Add(-time.Minute)
	full := func() []quota.Observation {
		return []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, FiveHour: &quota.Window{UsedPct: pct(95)}}}
	}
	ok := func() []quota.Observation {
		return []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, FiveHour: &quota.Window{UsedPct: pct(20)}}}
	}
	red := capacityOf("red", "MemAvailable 3000 MiB < 4096 MiB", "PSI full 12")
	unknown := func(context.Context) (Capacity, error) {
		return Capacity{}, errors.New("capacity provider: signal: killed")
	}
	cases := []struct {
		name     string
		env      AdmissionEnv
		mod      func(*Request)
		want     string
		reason   string
		warning  string
		noWayOut bool
	}{
		{name: "red", env: AdmissionEnv{Capacity: red}, want: Refuse,
			reason: "capacity red: MemAvailable 3000 MiB < 4096 MiB; PSI full 12 (to start anyway: " + wayOutFlag + ", or " + wayOutConfig + " for every spawn)"},
		{name: "red, robot", env: AdmissionEnv{Capacity: red, Quota: ok}, mod: func(r *Request) { r.RequireObservations = true }, want: Refuse,
			reason: "capacity red: MemAvailable 3000 MiB < 4096 MiB; PSI full 12 (to start anyway: "},
		{name: "red, forced", env: AdmissionEnv{Capacity: red}, mod: func(r *Request) { r.Force = true }, want: Refuse, reason: "capacity red"},
		{name: "red, ignored", env: AdmissionEnv{Capacity: red}, mod: func(r *Request) { r.IgnoreCapacity = true }, want: Admit,
			warning: "capacity red: MemAvailable 3000 MiB < 4096 MiB; PSI full 12 (overridden by --ignore-capacity)", noWayOut: true},
		{name: "red, robot, ignored", env: AdmissionEnv{Capacity: red, Quota: ok},
			mod: func(r *Request) { r.RequireObservations, r.IgnoreCapacity = true, true }, want: Admit,
			warning: "capacity red: MemAvailable 3000 MiB < 4096 MiB; PSI full 12 (overridden by --ignore-capacity)", noWayOut: true},
		{name: "unknown", env: AdmissionEnv{Capacity: unknown}, want: Admit,
			warning: "capacity unknown: capacity provider: signal: killed", noWayOut: true},
		{name: "unknown, robot", env: AdmissionEnv{Capacity: unknown, Quota: ok}, mod: func(r *Request) { r.RequireObservations = true }, want: Refuse,
			reason: "capacity unknown: capacity provider: signal: killed (to start anyway: " + wayOutFlag + ", or " + wayOutConfig},
		{name: "unknown, robot, ignored", env: AdmissionEnv{Capacity: unknown, Quota: ok},
			mod: func(r *Request) { r.RequireObservations, r.IgnoreCapacity = true, true }, want: Admit,
			warning: "capacity unknown: capacity provider: signal: killed (overridden by --ignore-capacity)", noWayOut: true},
		{name: "yellow", env: AdmissionEnv{Capacity: capacityOf("yellow", "load 9")}, want: Admit, warning: "capacity yellow: load 9", noWayOut: true},
		{name: "green, ignored", env: AdmissionEnv{Capacity: capacityOf("green")}, mod: func(r *Request) { r.IgnoreCapacity = true }, want: Admit, noWayOut: true},
		{name: "quota full, ignored", env: AdmissionEnv{Capacity: red, Quota: full}, mod: func(r *Request) { r.IgnoreCapacity = true }, want: Refuse,
			reason: "95%", warning: "(overridden by --ignore-capacity)", noWayOut: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := req(Want{Kind: "claude", Count: 1})
			if c.mod != nil {
				c.mod(&r)
			}
			c.env.Now = func() time.Time { return now }
			ad := Decide(context.Background(), c.env, r)
			warnings := strings.Join(ad.Warnings, "\n")
			if ad.Decision != c.want || !strings.Contains(ad.Reason, c.reason) || !strings.Contains(warnings, c.warning) {
				t.Fatalf("= %s %q, warnings %q; want %s %q, warning %q", ad.Decision, ad.Reason, warnings, c.want, c.reason, c.warning)
			}
			if c.noWayOut && strings.Contains(ad.Reason, "to start anyway") {
				t.Fatalf("reason %q names a way out of no capacity refusal", ad.Reason)
			}
		})
	}
}

// The robot form with --ignore-capacity (ACFS newproj on a small host):
// a red capacity refuses the robot spawn and creates nothing, its error
// names both ways out, and a rerun of the same --op-id with the override
// starts the agent, its warning in the admission.
func TestRobotSpawnIgnoresCapacity(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute)
	w.env.Admission = AdmissionEnv{Capacity: capacityOf("red", "MemAvailable 3000 MiB < 4096 MiB"),
		Quota: func() []quota.Observation {
			return []quota.Observation{{Pool: "claude", Kind: "claude", ObservedAt: &at, FiveHour: &quota.Window{UsedPct: pct(20)}}}
		}}
	r := req(Want{Kind: "claude", Count: 1})
	r.NoKickoff, r.RequireObservations, r.OperationID = true, true, "op-cap-1"

	res, err := Spawn(ctx, w.env, r)
	out := SpawnOutput(res, err, "json", time.Now())
	if !errors.Is(err, ErrRefused) || len(w.h.calls) != 0 || out.Success || out.ErrorCode != "ADMISSION_REFUSED" ||
		out.Admission.Decision != Refuse || !strings.Contains(out.Error, wayOutFlag) || !strings.Contains(out.Error, wayOutConfig) {
		t.Fatalf("refused robot spawn = %v, calls %v, out %+v", err, w.h.calls, out.RobotResponse)
	}

	r.IgnoreCapacity = true
	res, err = Spawn(ctx, w.env, r)
	out = SpawnOutput(res, err, "json", time.Now())
	if err != nil || !out.Success || out.Admission.Decision != Admit || len(out.Agents) != 1 || !out.Agents[0].Ready {
		t.Fatalf("robot spawn with --ignore-capacity = %v, out %+v", err, out)
	}
	if got := strings.Join(out.Admission.Warnings, "\n"); !strings.Contains(got,
		"capacity red: MemAvailable 3000 MiB < 4096 MiB (overridden by --ignore-capacity)") {
		t.Fatalf("warnings = %q", got)
	}
	if res.OperationID != "op-cap-1" || res.Replayed {
		t.Fatalf("operation = %q, replayed %v", res.OperationID, res.Replayed)
	}
}
