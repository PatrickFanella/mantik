package storage

import (
	"encoding/json"
	"reflect"
	"time"
)

type outcomeCustodyInput struct {
	Command    PaperDecisionCommand
	Basis      PaperDecisionBasis
	At         time.Time
	Membership bool
}

// This cache lives inside one already-read owner snapshot. It retains only
// successful verifications and at most MaxPaperOutcomeBytes of serialized keys.
// Command buckets require full equality, including clocks and membership mode.
// Serialize only new inputs for the byte budget, avoiding repeated large JSON keys.
func memoPaperOutcomeCustody(verify func(PaperDecisionCommand, PaperDecisionBasis, time.Time, bool) error) func(PaperDecisionCommand, PaperDecisionBasis, time.Time, bool) error {
	cache := map[PaperDecisionCommand][]outcomeCustodyInput{}
	retainedBytes := 0
	return func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, membership bool) error {
		input := outcomeCustodyInput{c, b, at, membership}
		for _, prior := range cache[c] {
			if reflect.DeepEqual(prior, input) {
				return nil
			}
		}
		raw, err := json.Marshal(input)
		// The verifier is the same trusted, read-only owner function used by
		// uncached replay. Preserve its exact Go inputs. Retain a detached key
		// only when JSON cloning preserves full equality (including clocks).
		original := clone(input)
		if err := verify(c, b, at, membership); err != nil {
			return err
		}
		if err == nil && len(raw) <= MaxPaperOutcomeBytes-retainedBytes && reflect.DeepEqual(original, input) {
			cache[c] = append(cache[c], original)
			retainedBytes += len(raw)
		}
		return nil
	}
}
