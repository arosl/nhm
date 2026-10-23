package send

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/herdr"
	"github.com/arosl/nhm/internal/integrations/cass"
)

// downCASS is a cass whose every search fails.
type downCASS struct{}

func (downCASS) Search(context.Context, cass.Search) ([]cass.Hit, error) {
	return nil, errors.New("index locked")
}
func (downCASS) IndexSession(context.Context, string, string, string) (string, error) {
	return "", errors.New("index locked")
}

// nhmView is the part of a robot answer that nhm-p6r adds under _nhm.
type nhmView struct {
	NHM struct {
		Targets []struct {
			Target     string                   `json:"target"`
			MailName   string                   `json:"mail_name"`
			Injections []enrich.TargetInjection `json:"injections"`
		} `json:"targets"`
		Memory enrich.Report `json:"memory_injection"`
		CASS   enrich.Report `json:"cass_injection"`
	} `json:"_nhm"`
}

// robotView decodes a robot answer as nhmView and as a plain map.
func robotView(t *testing.T, out any) (nhmView, map[string]any) {
	t.Helper()
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var v nhmView
	var raw map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return v, raw
}

// includedIDs is one source's included item ids in a target's injections.
func includedIDs(list []enrich.TargetInjection, source string) []string {
	return idsWith(list, source, enrich.Included)
}

// idsWith is one source's item ids of one status in a target's injections.
func idsWith(list []enrich.TargetInjection, source, status string) []string {
	ids := []string{}
	for _, ti := range list {
		if ti.Source != source {
			continue
		}
		for _, it := range ti.Items {
			if it.Status == status {
				ids = append(ids, it.ID)
			}
		}
	}
	return ids
}

// A stored send's receipt reads back what its enrichment gave each
// target: the CM rule ids and the CASS hit ids, under _nhm, as the send's
// own answer showed them. ntm's receipt fields stay as they are.
func TestReceiptReadsBackTheEnrichment(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.env.CM = &memCM{rules: twoRules}
	w.env.CASS = &memCASS{hits: []cass.Hit{{SourcePath: "/s/a.jsonl", LineNumber: 7, Content: "The login bug was a CSRF check.", Agent: "claude"}}}
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	req := memoryReq("op-k")
	req.Enrich.CASS = enrich.BestEffort
	res, err := Send(ctx, w.env, req)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := Stored(ctx, w.db, "op-k")
	if err != nil {
		t.Fatal(err)
	}
	receipt, raw := robotView(t, ReceiptOutput(stored, "json", time.Now()))
	if _, ok := raw["cass_injection"]; ok {
		t.Fatalf("the receipt has a cass_injection that ntm's has not: %v", raw)
	}
	tg := receipt.NHM.Targets
	if len(tg) != 1 || tg[0].Target != "0.1" {
		t.Fatalf("_nhm.targets = %+v", tg)
	}
	if got := includedIDs(tg[0].Injections, enrich.SourceCM); !reflect.DeepEqual(got, []string{"b-1", "b-2"}) {
		t.Fatalf("cm = %v", got)
	}
	if got := includedIDs(tg[0].Injections, enrich.SourceCASS); !reflect.DeepEqual(got, []string{"/s/a.jsonl:7"}) {
		t.Fatalf("cass = %v", got)
	}
	m, c := receipt.NHM.Memory, receipt.NHM.CASS
	if !m.Enabled || m.Mode != enrich.BestEffort || !c.Enabled || c.Mode != enrich.BestEffort || c.Error != "" || len(c.Items) != 1 {
		t.Fatalf("reports = %+v %+v", m, c)
	}
	// The send's answer showed the same.
	sent, _ := robotView(t, SendOutput(res, "json", time.Now()))
	if !reflect.DeepEqual(sent.NHM, receipt.NHM) {
		t.Fatalf("send _nhm = %+v\nreceipt _nhm = %+v", sent.NHM, receipt.NHM)
	}
}

// Each target's entry has its own items: a context that has the rules
// already gets them suppressed, a new one gets them included.
func TestReceiptGivesEachTargetItsOwnItems(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.env.CM = &memCM{rules: twoRules}
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	if _, err := Send(ctx, w.env, memoryReq("op-1")); err != nil {
		t.Fatal(err)
	}
	w.fake.add("w1:p1", "claude", "icyknoll", herdr.StatusIdle, 20)
	w.bind(t, "BlueLake", "w1:t1", "w1:p2", "claude", herdr.StatusIdle)
	if _, err := Send(ctx, w.env, memoryReq("op-2")); err != nil {
		t.Fatal(err)
	}
	stored, err := Stored(ctx, w.db, "op-2")
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := robotView(t, ReceiptOutput(stored, "json", time.Now()))
	want := map[string]string{"IcyKnoll": enrich.Suppressed, "BlueLake": enrich.Included}
	if len(receipt.NHM.Targets) != len(want) {
		t.Fatalf("_nhm.targets = %+v", receipt.NHM.Targets)
	}
	for _, tg := range receipt.NHM.Targets {
		status, ok := want[tg.MailName]
		if got := idsWith(tg.Injections, enrich.SourceCM, status); !ok || !reflect.DeepEqual(got, []string{"b-1", "b-2"}) {
			t.Fatalf("%s (%s): %s cm = %v; injections %+v", tg.MailName, tg.Target, status, got, tg.Injections)
		}
	}
}

// With CASS down, a best-effort kickoff still goes, and its receipt says
// why CASS gave nothing (E06's locked index).
func TestReceiptSaysWhyCASSGaveNothing(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.env.CM, w.env.CASS = &memCM{rules: twoRules}, downCASS{}
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	req := memoryReq("op-d")
	req.Enrich.CASS = enrich.BestEffort
	res, err := Send(ctx, w.env, req)
	if err != nil || res.Targets[0].Status != StatusSubmitted {
		t.Fatalf("send = %+v, %v", res, err)
	}
	stored, err := Stored(ctx, w.db, "op-d")
	if err != nil {
		t.Fatal(err)
	}
	receipt, raw := robotView(t, ReceiptOutput(stored, "json", time.Now()))
	if c := receipt.NHM.CASS; !c.Enabled || !strings.Contains(c.Error, "index locked") {
		t.Fatalf("_nhm.cass_injection = %+v", c)
	}
	tg := receipt.NHM.Targets[0]
	if got := includedIDs(tg.Injections, enrich.SourceCASS); len(got) != 0 {
		t.Fatalf("cass = %v", got)
	}
	if got := includedIDs(tg.Injections, enrich.SourceCM); len(got) != 2 {
		t.Fatalf("cm = %v", got)
	}
	// CASS's empty list is [], in the receipt and in the send's answer.
	items := func(raw map[string]any) any {
		target := raw["_nhm"].(map[string]any)["targets"].([]any)[0].(map[string]any)
		for _, ti := range target["injections"].([]any) {
			if ti.(map[string]any)["source"] == enrich.SourceCASS {
				return ti.(map[string]any)["items"]
			}
		}
		return "no cass entry"
	}
	_, sentRaw := robotView(t, SendOutput(res, "json", time.Now()))
	for what, r := range map[string]map[string]any{"receipt": raw, "send": sentRaw} {
		if got, ok := items(r).([]any); !ok || len(got) != 0 {
			t.Fatalf("%s: cass items = %#v", what, items(r))
		}
	}
}

// A send with no enrichment reports both sources off and gives no target
// an injections list.
func TestReceiptWithoutEnrichment(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.bind(t, "IcyKnoll", "w1:t1", "w1:p1", "claude", herdr.StatusIdle)
	if _, err := Send(ctx, w.env, base(Request{Message: "hi", OperationID: "op-p"})); err != nil {
		t.Fatal(err)
	}
	stored, err := Stored(ctx, w.db, "op-p")
	if err != nil {
		t.Fatal(err)
	}
	receipt, raw := robotView(t, ReceiptOutput(stored, "json", time.Now()))
	if m, c := receipt.NHM.Memory, receipt.NHM.CASS; !reflect.DeepEqual(m, notInjected) || c.Enabled || c.Mode != enrich.Off || c.SkippedReason != "off" {
		t.Fatalf("reports = %+v %+v", m, c)
	}
	target := raw["_nhm"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	if _, ok := target["injections"]; ok {
		t.Fatalf("target = %v", target)
	}
}
