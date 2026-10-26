package storage

import (
	"crypto/sha256"
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
// Digest buckets require full equality; neither a collision nor JSON omitting a
// Go-level distinction can reuse a different historical custody check.
func memoPaperOutcomeCustody(verify func(PaperDecisionCommand, PaperDecisionBasis, time.Time, bool) error) func(PaperDecisionCommand, PaperDecisionBasis, time.Time, bool) error {
	cache := map[[32]byte][]outcomeCustodyInput{}
	retainedBytes := 0
	return func(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, membership bool) error {
		input := outcomeCustodyInput{c, b, at, membership}
		raw, err := json.Marshal(input)
		key := sha256.Sum256(raw)
		if err == nil {
			for _, prior := range cache[key] {
				if reflect.DeepEqual(prior, input) {
					return nil
				}
			}
		}
		// The verifier is the same trusted, read-only owner function used by
		// uncached replay. Preserve its exact Go inputs. Retain a detached key
		// only when JSON cloning preserves full equality (including clocks).
		original := clone(input)
		if err := verify(c, b, at, membership); err != nil {
			return err
		}
		if err == nil && len(raw) <= MaxPaperOutcomeBytes-retainedBytes && reflect.DeepEqual(original, input) {
			cache[key] = append(cache[key], original)
			retainedBytes += len(raw)
		}
		return nil
	}
}
