package cli

import (
	"errors"

	"github.com/arosl/nhm/internal/core/enrich"
	"github.com/arosl/nhm/internal/core/send"
	"github.com/arosl/nhm/internal/integrations/cass"
	"github.com/arosl/nhm/internal/integrations/cm"
)

// loadEnrichConfig reads ~/.config/nhm/config.toml for the enrichment.
func loadEnrichConfig() (enrich.Config, error) {
	path, err := enrich.ConfigPath()
	if err != nil {
		return enrich.Config{}, err
	}
	return enrich.LoadConfig(path)
}

// cmRunner and cassRunner make the runners of the cm and cass binaries;
// tests replace them.
var (
	cmRunner   = cm.Exec
	cassRunner = cass.Exec
)

// enrichClients are the cm and cass clients and the budgets of a config.
func enrichClients(cfg enrich.Config) (enrich.CM, enrich.CASS, enrich.Budgets) {
	return cm.Client{Run: cmRunner(cfg.CMBinary)}, cass.Client{Run: cassRunner(cfg.CASS.Binary)}, cfg.Budgets()
}

// enrichment resolves the send flags. Conflicting flags refuse; a flag
// that is not given leaves the template and the config to decide.
func (f sendFlags) enrichment(cfg enrich.Config) (send.Enrichment, error) {
	e := send.Enrichment{DefaultCM: cfg.Memory.CM, DefaultCASS: cfg.CASS.Context, Query: f.memoryQuery,
		CheckDays: cfg.CASS.CheckDays, Check: cfg.CheckOn()}
	switch {
	case f.withCASS && f.noCASS:
		return e, errors.New("give --with-cass or --no-cass, not both")
	case f.cassCheck && f.noCassCheck:
		return e, errors.New("give --cass-check or --no-cass-check, not both")
	case f.cassCheck && f.noCASS:
		return e, errors.New("--no-cass turns the duplicate check off; drop --cass-check")
	}
	if f.memorySet {
		e.CM = enrich.Off
		if f.withMemory {
			e.CM = enrich.BestEffort
		}
	}
	switch {
	case f.withCASS:
		e.CASS = enrich.BestEffort
	case f.noCASS:
		e.CASS = enrich.Off
	}
	switch {
	case f.cassCheck:
		e.Check = true
	case f.noCassCheck, f.noCASS:
		e.Check = false
	}
	return e, nil
}
