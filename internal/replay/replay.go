package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/library"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

type Pins struct {
	StrategyID      string `json:"strategy_id"`
	ContractVersion string `json:"contract_version"`
	RecipeID        string `json:"recipe_id"`
	RecipeVersion   string `json:"recipe_version"`
}

func FixturePins() Pins {
	e := library.FixtureEntry()
	return Pins{e.ID, e.ContractVersion, e.Evaluation.ID, e.Evaluation.Version}
}

type Exposure struct {
	Key         string    `json:"key"`
	Revision    int       `json:"revision"`
	SHA256      string    `json:"sha256"`
	VisibleAt   time.Time `json:"visible_at"`
	Disposition string    `json:"disposition"`
}

type Frame struct {
	Cutoff           time.Time       `json:"cutoff"`
	AdmissionBlocked bool            `json:"admission_blocked"`
	Evidence         []Exposure      `json:"evidence"`
	Snapshot         domain.Snapshot `json:"snapshot"`
}

type Result struct {
	DatasetID      string  `json:"dataset_id"`
	DatasetVersion string  `json:"dataset_version"`
	DatasetSHA256  string  `json:"dataset_sha256"`
	EvidenceClass  string  `json:"evidence_class"`
	Pins           Pins    `json:"pins"`
	Frames         []Frame `json:"frames"`
}

// Clock has no wall-clock fallback. Each run owns an explicit, monotonic clock.
type Clock struct{ now time.Time }

func (c *Clock) Advance(at time.Time) error {
	if at.IsZero() || !c.now.IsZero() && !at.After(c.now) {
		return errors.New("replay cutoffs must be nonzero and strictly increasing")
	}
	c.now = at.UTC()
	return nil
}

func (c *Clock) Now() time.Time { return c.now }

// Run processes records forward in availability order. Corrections retain
// evidence and block further admission; they never rewrite finalized economics.
func Run(ctx context.Context, d Dataset, pins Pins, cutoffs []time.Time) (Result, error) {
	if err := d.Validate(); err != nil {
		return Result{}, err
	}
	if pins != FixturePins() {
		return Result{}, errors.New("unsupported strategy or evaluation pins")
	}
	if len(cutoffs) == 0 || len(cutoffs) > 10000 {
		return Result{}, errors.New("provide 1–10000 replay cutoffs")
	}
	// Validate all cutoffs before any evaluation and keep caller storage untouched.
	var clock Clock
	for _, cutoff := range cutoffs {
		if err := clock.Advance(cutoff); err != nil {
			return Result{}, err
		}
	}
	clock = Clock{}
	records := slices.Clone(d.Records)
	slices.SortFunc(records, func(a, b Record) int {
		if order := a.visibleAt().Compare(b.visibleAt()); order != 0 {
			return order
		}
		if a.Key < b.Key {
			return -1
		}
		if a.Key > b.Key {
			return 1
		}
		return a.Revision - b.Revision
	})
	store := storage.NewMemory()
	defer store.Close()
	result := Result{DatasetID: d.ID, DatasetVersion: d.Version, DatasetSHA256: d.SHA256, EvidenceClass: d.EvidenceClass, Pins: pins, Frames: []Frame{}}
	seen := map[string]bool{}
	evidence := []Exposure{}
	blocked, cursor := false, 0
	resultBytes := 0
	for _, cutoff := range cutoffs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		_ = clock.Advance(cutoff)
		// A known gap at this clock tick must block all actions at the same tick,
		// regardless of arbitrary source-key ordering.
		end := cursor
		for end < len(records) && !records[end].visibleAt().After(clock.Now()) {
			end++
		}
		for cursor < end {
			groupEnd := cursor + 1
			for groupEnd < end && records[groupEnd].visibleAt().Equal(records[cursor].visibleAt()) {
				groupEnd++
			}
			for i := cursor; i < groupEnd; i++ {
				if records[i].Quality != "usable" || records[i].Revision > 1 {
					blocked = true
				}
			}
			for ; cursor < groupEnd; cursor++ {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
				r := records[cursor]
				id := fmt.Sprintf("%s-r%d", r.Key, r.Revision)
				disposition := "replayed"
				switch {
				case seen[id]:
					disposition = "duplicate"
				case r.Quality != "usable":
					disposition = r.Quality
				case r.Revision > 1:
					disposition = "correction_review_required"
				case blocked:
					disposition = "admission_blocked"
				default:
					o, err := r.quote()
					if err != nil {
						return Result{}, err
					}
					o.ID = id
					// Preserve original times in the dataset. Operational freshness uses
					// when this replay could first consume the observation.
					o.ReceivedAt = r.visibleAt().UTC()
					if _, err := store.Ingest(ctx, o); err != nil {
						return Result{}, err
					}
				}
				seen[id] = true
				evidence = append(evidence, Exposure{r.Key, r.Revision, r.SHA256, r.visibleAt().UTC(), disposition})
			}
		}
		snapshot, err := store.Snapshot(ctx)
		if err != nil {
			return Result{}, err
		}
		frame := Frame{clock.Now(), blocked, slices.Clone(evidence), snapshot}
		raw, err := json.Marshal(frame)
		if err != nil {
			return Result{}, err
		}
		resultBytes += len(raw)
		if resultBytes > 32<<20 {
			return Result{}, errors.New("replay frames exceed 32 MiB; reduce cutoffs or dataset size")
		}
		result.Frames = append(result.Frames, frame)
	}
	return result, nil
}

// FixtureDataset builds the same four synthetic observations as the demo. It is
// an exportable dataset fixture, never historical or prospective qualification.
func FixtureDataset() (Dataset, error) {
	d := Dataset{Schema: DatasetSchema, ID: "demo-quotes", Version: "v1", EvidenceClass: "synthetic_unqualified"}
	for _, o := range workflow.Fixtures() {
		raw, err := json.Marshal(o)
		if err != nil {
			return Dataset{}, err
		}
		d.Records = append(d.Records, Record{Key: o.ID, Revision: 1, OccurredAt: o.OccurredAt, AvailableAt: o.ReceivedAt, ReceivedAt: o.ReceivedAt, Original: string(raw), SHA256: Digest(string(raw)), Instrument: Mapping{"DEMO", "DEMO", "fixture-map-v1"}, Access: Access{"mantik:synthetic-demo-v1", "GPL-3.0-only", "mantik-fixture"}, Quality: "usable"})
	}
	return Seal(d)
}
