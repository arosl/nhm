package ensemble

import (
	"errors"
	"strings"
	"testing"
)

// ntm's data, as taken: 80 modes in twelve categories, 9 presets that all
// resolve (F.13).
func TestBuiltinCatalog(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(c.Modes("")); n != 80 {
		t.Errorf("modes = %d, want 80", n)
	}
	core, advanced := len(c.Modes(TierCore)), len(c.Modes(TierAdvanced))
	if core == 0 || advanced == 0 || core+advanced+len(c.Modes(TierExperimental)) != 80 {
		t.Errorf("tiers: core %d, advanced %d", core, advanced)
	}
	cats := map[ModeCategory]int{}
	for _, m := range c.Modes("") {
		cats[m.Category]++
	}
	if len(cats) != 12 {
		t.Errorf("categories = %d, want 12", len(cats))
	}
	presets := c.Presets()
	if len(presets) != 9 {
		t.Fatalf("presets = %d, want 9", len(presets))
	}
	for i := 1; i < len(presets); i++ {
		if presets[i-1].Name >= presets[i].Name {
			t.Errorf("presets not sorted: %s, %s", presets[i-1].Name, presets[i].Name)
		}
	}
	p, err := c.Preset("project-diagnosis")
	if err != nil {
		t.Fatal(err)
	}
	modes, err := c.Resolve(p)
	if err != nil || len(modes) != 5 || modes[0].ID != "systems-thinking" {
		t.Fatalf("project-diagnosis = %+v %v", modes, err)
	}
}

func TestModeLookup(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	byID, err := c.Mode("deductive")
	if err != nil || byID.Code != "A1" || byID.Category != CategoryFormal {
		t.Fatalf("deductive = %+v %v", byID, err)
	}
	byCode, err := c.Mode("a1")
	if err != nil || byCode.ID != "deductive" {
		t.Fatalf("a1 = %+v %v", byCode, err)
	}
	if _, err := c.Mode("telepathy"); !errors.Is(err, ErrUnknownMode) {
		t.Fatalf("unknown mode: %v", err)
	}
	if _, err := c.Preset("nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("unknown preset: %v", err)
	}
}

func TestCatalogRefusesBadData(t *testing.T) {
	good := ReasoningMode{ID: "deductive", Code: "A1", Name: "D", Category: CategoryFormal, Tier: TierCore, ShortDesc: "x"}
	adv := ReasoningMode{ID: "bayesian", Code: "C1", Name: "B", Category: CategoryUncertainty, Tier: TierAdvanced, ShortDesc: "y"}
	cases := map[string]struct {
		modes   []ReasoningMode
		presets []EnsemblePreset
		want    string
	}{
		"duplicate id":        {[]ReasoningMode{good, good}, nil, "twice"},
		"code of another cat": {[]ReasoningMode{{ID: "x", Code: "B1", Name: "X", Category: CategoryFormal, Tier: TierCore, ShortDesc: "x"}}, nil, "does not match"},
		"advanced not allowed": {[]ReasoningMode{good, adv},
			[]EnsemblePreset{{Name: "p", Modes: []ModeRef{ModeRefFromID("bayesian")}}}, "does not allow advanced"},
		"unknown mode in preset": {[]ReasoningMode{good},
			[]EnsemblePreset{{Name: "p", Modes: []ModeRef{ModeRefFromID("nope")}}}, "unknown mode"},
		"mode twice in preset": {[]ReasoningMode{good},
			[]EnsemblePreset{{Name: "p", Modes: []ModeRef{ModeRefFromID("deductive"), {Code: "A1"}}}}, "twice"},
	}
	for name, tc := range cases {
		if _, err := NewCatalog(tc.modes, tc.presets); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewCatalog([]ReasoningMode{good, adv},
		[]EnsemblePreset{{Name: "p", AllowAdvanced: true, Modes: []ModeRef{ModeRefFromID("bayesian")}}}); err != nil {
		t.Errorf("an advanced mode with AllowAdvanced: %v", err)
	}
}
