package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Errors of a Guard.
var (
	// ErrNotReviewed: the request is not the one reviewed (it changed
	// after the review, or there was none); review it again.
	ErrNotReviewed = errors.New("actions: the request changed since its review; review it again")
	// ErrSubmitted: the review was submitted already.
	ErrSubmitted = errors.New("actions: already submitted; its result is in recent operations")
)

// Guard is one submission per review (plan-v4 section 4). Review fixes a
// request's operation id; Submit hands that id out once. Every shortcut
// to the same review shares the guard: a second submit is refused, and a
// request that changed needs a new review, with a new id.
//
// A submitted request is held until its answer came (Settle): until then a
// new review of the same request is refused too, so two quick submits of
// the same text never become two operations. A lost answer keeps the
// hold: the operation may run on ("unknown" until the core reconciles
// it), and this guard never makes a second id for it.
type Guard struct {
	mu        sync.Mutex
	newID     func() (string, error)
	key       string
	opID      string
	submitted bool
	// held are the submitted requests whose answer did not come yet, by
	// key, with their operation id.
	held map[string]string
}

// NewGuard returns a guard; newID makes operation ids (store.NewOperationID
// in production).
func NewGuard(newID func() (string, error)) *Guard {
	return &Guard{newID: newID, held: map[string]string{}}
}

// Review starts the review of the request with key (ReviewKey) and
// returns its operation id. While that review is not submitted, the same
// key keeps the same id (a redraw); any other key starts a new one. A
// request that is held (submitted, its answer not in) is ErrSubmitted.
func (g *Guard) Review(key string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if id, ok := g.held[key]; ok {
		return "", fmt.Errorf("%w (operation %s)", ErrSubmitted, id)
	}
	if key == g.key && g.opID != "" && !g.submitted {
		return g.opID, nil
	}
	id, err := g.newID()
	if err != nil {
		return "", err
	}
	g.key, g.opID, g.submitted = key, id, false
	return id, nil
}

// Submit returns the operation id of the review of key, once, and holds
// the request until Settle.
func (g *Guard) Submit(key string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.held[key] != "":
		return "", fmt.Errorf("%w (operation %s)", ErrSubmitted, g.held[key])
	case g.opID == "" || key != g.key:
		return "", ErrNotReviewed
	case g.submitted:
		return "", ErrSubmitted
	}
	g.submitted = true
	if g.held == nil {
		g.held = map[string]string{}
	}
	g.held[key] = g.opID
	return g.opID, nil
}

// Settle ends the hold of a submitted request once its answer came (err
// is the answer's error): the same request may then be reviewed again, as
// a new operation. A lost answer (NoAnswer) keeps the hold. A surface
// calls it where the answer arrives, also when its form was closed.
func (g *Guard) Settle(key string, err error) {
	if NoAnswer(err) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.held, key)
}

// ReviewKey is a request's identity for the guard: the action, the target
// set (in any order) and the form's values. The context's other fields do
// not count: one surface reviews within one context.
func ReviewKey(actionID string, req Request) string {
	targets := make([]string, len(req.Targets))
	for i, t := range req.Targets {
		targets[i] = t.Key()
	}
	slices.Sort(targets)
	params := make([]string, 0, len(req.Params))
	for k, v := range req.Params {
		params = append(params, k+"="+v)
	}
	slices.Sort(params)
	h := sha256.New()
	for _, part := range [][]string{{actionID}, targets, params} {
		for _, s := range part {
			// Length-prefixed, so no two different requests write the
			// same bytes.
			h.Write([]byte{byte(len(s) >> 24), byte(len(s) >> 16), byte(len(s) >> 8), byte(len(s))})
			h.Write([]byte(s))
		}
		h.Write([]byte{0xff, 0xff, 0xff, 0xff})
	}
	return actionID + ":" + strings.ToLower(hex.EncodeToString(h.Sum(nil))[:24])
}
