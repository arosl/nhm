package cli

import (
	"context"
	"fmt"

	"github.com/arosl/nhm/internal/core/snapshot"
	"github.com/arosl/nhm/internal/ui/picker"
)

// pickerSource is the agent picker's snapshot (nhm-qbi.3): the same
// in-process build as nhm status, without mail and beads, read again on
// each refresh. A herdr that did not answer is an error, never an empty
// workspace. The returned close closes the instance's database.
func pickerSource(socket string) (picker.Source, func(), error) {
	r, err := openReader(socket, false)
	if err != nil {
		return nil, nil, err
	}
	src := func(ctx context.Context) (*snapshot.Model, error) {
		m := r.model(ctx, "")
		if !m.HerdrOK() {
			return nil, fmt.Errorf("herdr did not answer: %s", m.Sources[snapshot.SourceHerdr].Error)
		}
		return m, nil
	}
	return src, r.close, nil
}
