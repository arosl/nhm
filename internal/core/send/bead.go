package send

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// beadTimeout bounds br show.
const beadTimeout = 10 * time.Second

// BrShow reads a bead with `br show <id> --json` in the project root, as
// ntm's send --bead does. It never reads the caller's directory.
func BrShow(ctx context.Context, projectRoot, id string) (Bead, error) {
	if projectRoot == "" {
		return Bead{}, fmt.Errorf("no project to read the bead in")
	}
	ctx, cancel := context.WithTimeout(ctx, beadTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "br", "show", id, "--json")
	cmd.Dir = projectRoot
	// A child of br (or of a wrapper script) that holds stdout open must
	// not keep the call past beadTimeout (nhm-fty): after WaitDelay the
	// pipes are closed.
	cmd.WaitDelay = time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Bead{}, fmt.Errorf("br show %s: %w: %s", id, err, strings.TrimSpace(stderr.String()))
	}
	return parseBead(out, id)
}

// parseBead reads br show --json: an array with the bead, or the bead.
func parseBead(out []byte, id string) (Bead, error) {
	var list []Bead
	if err := json.Unmarshal(out, &list); err != nil {
		var one Bead
		if err := json.Unmarshal(out, &one); err != nil {
			return Bead{}, fmt.Errorf("br show %s: %w", id, err)
		}
		list = []Bead{one}
	}
	for _, b := range list {
		if b.ID == id {
			return b, nil
		}
	}
	return Bead{}, fmt.Errorf("br show %s: the bead is not in the answer", id)
}
