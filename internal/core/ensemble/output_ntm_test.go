// The render tests are derived from ntm (github.com/Dicklesworthstone/ntm)
// internal/cli/modes_test.go and internal/cli/ensemble_presets_test.go at
// commit 9d5ed977; see LICENSE in this directory and PROVENANCE.md. Local
// changes: nhm's names (RenderModesList for renderModesList, …), and the
// presets built from nhm's catalog. The tests after them are nhm's.

package ensemble

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRenderModesList_JSON(t *testing.T) {
	payload := ModesListOutput{
		GeneratedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Modes: []ModesListRow{
			{ID: "deductive", Code: "A1", Name: "Deductive", Category: "Formal", Tier: "core", ShortDesc: "Formal logic"},
			{ID: "inductive", Code: "A2", Name: "Inductive", Category: "Causal", Tier: "advanced", ShortDesc: "Pattern-based"},
		},
		Count:  2,
		Filter: "tier=core",
	}

	var buf bytes.Buffer
	if err := RenderModesList(&buf, payload, "json"); err != nil {
		t.Fatalf("RenderModesList(json): %v", err)
	}
	var decoded ModesListOutput
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	if decoded.Count != 2 || len(decoded.Modes) != 2 {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestRenderModesList_YAML(t *testing.T) {
	payload := ModesListOutput{
		GeneratedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Modes:       []ModesListRow{{ID: "abductive", Code: "B1", Name: "Abductive", Category: "Causal", Tier: "core"}},
		Count:       1,
	}

	var buf bytes.Buffer
	if err := RenderModesList(&buf, payload, "yaml"); err != nil {
		t.Fatalf("RenderModesList(yaml): %v", err)
	}
	var decoded ModesListOutput
	if err := yaml.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("YAML decode: %v", err)
	}
	if decoded.Count != 1 {
		t.Errorf("Count = %d, want 1", decoded.Count)
	}
}

func TestRenderModesList_Text(t *testing.T) {
	payload := ModesListOutput{
		Modes: []ModesListRow{{ID: "deductive", Code: "A1", Name: "Deductive", Category: "Formal", Tier: "core"}},
		Count: 1,
	}

	var buf bytes.Buffer
	if err := RenderModesList(&buf, payload, "text"); err != nil {
		t.Fatalf("RenderModesList(text): %v", err)
	}
	// ntm's table: two spaces, columns as wide as their widest cell.
	want := "  CODE  ID         NAME       CATEGORY  TIER\n" +
		"  ----  ---------  ---------  --------  ----\n" +
		"  A1    deductive  Deductive  Formal    core\n" +
		"\n1 modes total\n"
	if got := buf.String(); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
}

func TestRenderModesList_TextEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderModesList(&buf, ModesListOutput{Modes: []ModesListRow{}}, "text"); err != nil {
		t.Fatalf("RenderModesList(text empty): %v", err)
	}
	if buf.String() != "No modes found matching criteria\n" {
		t.Errorf("empty = %q", buf.String())
	}
}

func TestRenderModesExplain_JSON(t *testing.T) {
	payload := ModesExplainOutput{
		GeneratedAt: time.Now().UTC(),
		Card:        &ModeCard{ModeID: "deductive", Code: "A1", Name: "Deductive", Category: "Formal", Tier: "core"},
	}

	var buf bytes.Buffer
	if err := RenderModesExplain(&buf, payload, "json"); err != nil {
		t.Fatalf("RenderModesExplain(json): %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON decode: %v", err)
	}
	card := decoded["card"].(map[string]interface{})
	if card["mode_id"] != "deductive" {
		t.Errorf("card.mode_id = %v, want deductive", card["mode_id"])
	}
}

func TestRenderModesExplain_YAML(t *testing.T) {
	payload := ModesExplainOutput{GeneratedAt: time.Now().UTC(), Card: &ModeCard{ModeID: "abductive", Code: "B1", Name: "Abductive"}}

	var buf bytes.Buffer
	if err := RenderModesExplain(&buf, payload, "yaml"); err != nil {
		t.Fatalf("RenderModesExplain(yaml): %v", err)
	}
	// ntm's ModeCard has JSON tags only, so yaml.v3 names the keys itself.
	if out := buf.String(); !strings.Contains(out, "modeid: abductive") || !strings.Contains(out, "generated_at:") {
		t.Errorf("yaml = %q", out)
	}
}

func TestRenderModesExplain_Text(t *testing.T) {
	payload := ModesExplainOutput{Card: &ModeCard{ModeID: "deductive", Code: "A1", Name: "Deductive", Category: "Formal",
		Tier: "core", ShortDesc: "Apply formal deductive logic"}}

	var buf bytes.Buffer
	if err := RenderModesExplain(&buf, payload, "text"); err != nil {
		t.Fatalf("RenderModesExplain(text): %v", err)
	}
	want := "📋 Deductive (A1)\n   ID: deductive | Category: Formal | Tier: core\n\nDescription:\n   Apply formal deductive logic\n\n"
	if buf.String() != want {
		t.Errorf("text = %q, want %q", buf.String(), want)
	}
}

func TestRunEnsemblePresets_Table(t *testing.T) {
	c := builtin(t)
	var buf bytes.Buffer
	if err := RenderPresets(&buf, PresetsList(c, "", false, time.Now()), false, "table"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "DISPLAY", "project-diagnosis", "idea-forge", "Total:", "embedded"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}

func TestRunEnsemblePresets_Verbose(t *testing.T) {
	c := builtin(t)
	var buf bytes.Buffer
	if err := RenderPresets(&buf, PresetsList(c, "", true, time.Now()), true, "table"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Modes (", "Synthesis:", "Budget:", "Min Confidence:", "Tokens/Mode:"} {
		if !strings.Contains(out, want) {
			t.Errorf("verbose lacks %q", want)
		}
	}
}

func TestRunEnsemblePresets_TagFilter(t *testing.T) {
	result := PresetsList(builtin(t), "debugging", false, time.Now())
	if result.Count < 2 {
		t.Errorf("expected at least 2 presets with 'debugging' tag, got %d", result.Count)
	}
	for _, p := range result.Presets {
		if !containsString(p.Tags, "debugging") {
			t.Errorf("preset %q doesn't have 'debugging' tag: %v", p.Name, p.Tags)
		}
	}
}

func TestRunEnsemblePresets_VerboseJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderPresets(&buf, PresetsList(builtin(t), "", true, time.Now()), true, "json"); err != nil {
		t.Fatal(err)
	}
	var result PresetsOutput
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}
	if len(result.Details) == 0 || len(result.Presets) != 0 {
		t.Fatal("verbose JSON mode should populate details, not presets")
	}
	d := result.Details[0]
	if len(d.Modes) == 0 || d.Synthesis.Strategy == "" || d.Budget.MaxTotalTokens == 0 {
		t.Errorf("first detail = %+v", d)
	}
	if m := d.Modes[0]; m.ID == "" || m.Code == "" || m.Name == "" {
		t.Errorf("mode detail not resolved: %+v", m)
	}
}

func TestRenderPresetsTable_Empty(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		var buf bytes.Buffer
		if err := RenderPresets(&buf, PresetsOutput{}, verbose, "table"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "No ensemble presets found") {
			t.Errorf("verbose=%v: %q", verbose, buf.String())
		}
	}
}

// nhm's: the real catalog through ntm's shapes.

func builtin(t *testing.T) *Catalog {
	t.Helper()
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestModesListFromTheCatalog(t *testing.T) {
	c := builtin(t)
	now := time.Date(2026, 10, 12, 6, 0, 0, 0, time.FixedZone("x", 3600))
	core := ModesList(c, "", "", false, now)
	if core.Count != len(c.Modes(TierCore)) || core.Filter != "" || !core.GeneratedAt.Equal(now) || core.GeneratedAt.Location() != time.UTC {
		t.Errorf("default = %d modes, filter %q, at %v", core.Count, core.Filter, core.GeneratedAt)
	}
	for _, m := range core.Modes {
		if m.Tier != "core" || m.ShortDesc == "" {
			t.Errorf("default listed %+v", m)
		}
	}
	all := ModesList(c, "", "", true, now)
	if all.Count != 80 {
		t.Errorf("--all = %d", all.Count)
	}
	adv := ModesList(c, "advanced", "formal", false, now)
	if adv.Filter != "category=formal, tier=advanced" {
		t.Errorf("filter = %q", adv.Filter)
	}
	for _, m := range adv.Modes {
		if m.Tier != "advanced" || m.Category != "Formal" {
			t.Errorf("advanced formal listed %+v", m)
		}
	}
	if none := ModesList(c, "legendary", "", false, now); none.Count != 0 || none.Filter != "tier=legendary" {
		t.Errorf("an unknown tier = %+v", none)
	}
}

func TestModeCardFromTheCatalog(t *testing.T) {
	c := builtin(t)
	card, err := c.ModeCard("A1")
	if err != nil {
		t.Fatal(err)
	}
	if card.ModeID != "deductive" || card.TypicalCost != 3000 || len(card.Examples) != 2 || card.Icon != "" {
		t.Errorf("card = %+v", card)
	}
	// Formal pairs with Ampliative and Meta: one core mode of each.
	if len(card.Complements) != 2 {
		t.Fatalf("complements = %v", card.Complements)
	}
	for i, cat := range []ModeCategory{CategoryAmpliative, CategoryMeta} {
		m, err := c.Mode(card.Complements[i])
		if err != nil || m.Category != cat || m.Tier != TierCore {
			t.Errorf("complement %d = %+v %v", i, m, err)
		}
	}
	text := FormatCard(card)
	for _, want := range []string{"📋 ", "(A1)", "Example prompts:", "Estimated tokens: ~3000", "Pairs well with: "} {
		if !strings.Contains(text, want) {
			t.Errorf("card text lacks %q", want)
		}
	}
	if _, err := c.ModeCard("telepathy"); err == nil {
		t.Error("an unknown mode gave a card")
	}
}

func TestPresetsInNTMsOrderAndSource(t *testing.T) {
	c := builtin(t)
	out := PresetsList(c, "", false, time.Now())
	if out.Count != len(EmbeddedEnsembles) {
		t.Fatalf("count = %d", out.Count)
	}
	for i, p := range out.Presets {
		if p.Name != EmbeddedEnsembles[i].Name || p.Source != "embedded" || p.ModeCount != len(p.ModeCodes) {
			t.Errorf("preset %d = %+v, want %s", i, p, EmbeddedEnsembles[i].Name)
		}
		for _, code := range p.ModeCodes {
			if _, err := c.Mode(code); err != nil {
				t.Errorf("%s: mode code %s does not resolve", p.Name, code)
			}
		}
	}
	// ntm's tag filter is exact: "Debugging" is not "debugging".
	if n := PresetsList(c, "Debugging", false, time.Now()).Count; n != 0 {
		t.Errorf("tag Debugging = %d presets", n)
	}
}
