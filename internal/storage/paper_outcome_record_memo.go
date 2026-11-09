package storage

import (
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// A catalogue already checks every row's actual source custody. Reuse a native
// constructor result only for an exactly equal schema, basis and UTC clock.
// Only the request ID may differ; commands and final envelopes remain checked
// per row. Keys are detached and the cache stays inside this catalogue read.
func memoPaperOutcomeRecords(eval PaperOutcomeEvaluator) func(string, PaperOutcomeCommand, PaperOutcomeBasis, time.Time) (PaperOutcomeRecord, error) {
	missingEvaluator := eval == nil
	type input struct {
		Schema string
		Basis  PaperOutcomeBasis
		At     time.Time
	}
	type result struct {
		input  input
		record PaperOutcomeRecord
	}
	type bucket struct {
		schema, decision string
		at               time.Time
	}
	cache := map[bucket][]result{}
	bytes := 0
	return func(schema string, c PaperOutcomeCommand, b PaperOutcomeBasis, at time.Time) (PaperOutcomeRecord, error) {
		if missingEvaluator {
			return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
		}
		keyInput := input{schema, b, at.UTC()}
		key := bucket{schema, b.Decision.SHA256, at.UTC()}
		for _, old := range cache[key] {
			scope := old.record.Command
			scope.ID = c.ID
			if scope != c || !reflect.DeepEqual(old.input, keyInput) {
				continue
			}
			if c.Validate() != nil {
				return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
			}
			r := old.record
			r.Command = c
			r.SHA256 = ""
			r.SHA256 = workerplan.Hash(r)
			if r.Validate() != nil {
				return PaperOutcomeRecord{}, ErrPaperOutcomeInvalid
			}
			return r, nil
		}
		raw, marshalErr := json.Marshal(keyInput)
		canRetain := marshalErr == nil && len(raw) <= MaxPaperOutcomeBytes-bytes
		var original input
		if canRetain {
			original = clone(keyInput)
		}
		r, err := paperOutcomeForSchema(schema, c, b, at, eval)
		if err != nil {
			return PaperOutcomeRecord{}, err
		}
		if canRetain && reflect.DeepEqual(original, keyInput) {
			recordBytes, err := json.Marshal(r)
			if err == nil && len(raw)+len(recordBytes) <= MaxPaperOutcomeBytes-bytes {
				cache[key] = append(cache[key], result{original, r})
				bytes += len(raw) + len(recordBytes)
			}
		}
		return r, nil
	}
}
