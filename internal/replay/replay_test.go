package replay

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

func fixture(t *testing.T) Dataset {
	t.Helper()
	d, err := FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func seal(t *testing.T, d Dataset) Dataset {
	t.Helper()
	d, err := Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func run(t *testing.T, d Dataset, cutoffs ...time.Time) Result {
	t.Helper()
	r, err := Run(context.Background(), d, FixturePins(), cutoffs)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCutoffVisibilityAndFixtureParity(t *testing.T) {
	d := fixture(t)
	cutoffs := []time.Time{d.Records[0].AvailableAt.Add(-time.Second), d.Records[1].AvailableAt, d.Records[3].AvailableAt}
	r := run(t, d, cutoffs...)
	if len(r.Frames[0].Evidence) != 0 || r.Frames[0].Snapshot.ObservationCount != 0 {
		t.Fatal("future evidence leaked")
	}
	if r.Frames[1].Snapshot.ObservationCount != 2 || r.Frames[1].Snapshot.State.Case.Status != "open" {
		t.Fatal("inclusive cutoff missed the entry")
	}
	last := r.Frames[2].Snapshot
	if last.ObservationCount != 4 || len(last.Events) != 10 || last.State.Case.Status != "closed" || last.State.Case.Exit.RealizedPnLCents != -6000 {
		t.Fatalf("fixture parity failed: %+v", last)
	}
	again := run(t, d, cutoffs...)
	if !reflect.DeepEqual(r, again) {
		t.Fatal("identical replay diverged")
	}
	single := run(t, d, cutoffs[2])
	if !reflect.DeepEqual(last, single.Frames[0].Snapshot) {
		t.Fatal("cutoff partition changed decisions")
	}
	// A frame must not share mutable pointers with a later snapshot.
	r.Frames[1].Snapshot.State.Case.Entry.PriceCents = 1
	if r.Frames[2].Snapshot.State.Case.Entry.PriceCents != 10000 {
		t.Fatal("frames share state")
	}
}

func TestAvailabilityAndReceiptBothGateExposure(t *testing.T) {
	for _, field := range []string{"availability", "receipt"} {
		t.Run(field, func(t *testing.T) {
			d := fixture(t)
			r := &d.Records[1]
			originalCutoff := r.AvailableAt
			if field == "availability" {
				r.AvailableAt = r.AvailableAt.Add(30 * time.Second)
			} else {
				r.ReceivedAt = r.ReceivedAt.Add(30 * time.Second)
				var o domain.Observation
				_ = json.Unmarshal([]byte(r.Original), &o)
				o.ReceivedAt = r.ReceivedAt
				raw, _ := json.Marshal(o)
				r.Original, r.SHA256 = string(raw), Digest(string(raw))
			}
			d = seal(t, d)
			result := run(t, d, originalCutoff, originalCutoff.Add(30*time.Second))
			if len(result.Frames[0].Evidence) != 1 || result.Frames[0].Snapshot.State.Case.Entry != nil {
				t.Fatal("evidence arrived before both clocks permitted it")
			}
			if result.Frames[1].Snapshot.State.Case.Entry == nil {
				t.Fatal("visible fresh input was not evaluated")
			}
		})
	}
}

func TestLateArrivalAbstains(t *testing.T) {
	d := fixture(t)
	d.Records[1].AvailableAt = d.Records[3].AvailableAt.Add(time.Minute)
	d = seal(t, d)
	r := run(t, d, d.Records[1].AvailableAt)
	last := r.Frames[0].Snapshot
	if last.State.Case.Entry != nil {
		t.Fatal("late input opened a position")
	}
	decision := last.Events[len(last.Events)-1].Decision
	if decision == nil || decision.Action != "abstain" || decision.ReasonCode != "unusable_observation" {
		t.Fatal("late arrival did not retain an abstention")
	}
	if decision.Evaluations[0].Passed || decision.Evaluations[1].Passed {
		t.Fatal("late input passed freshness or event ordering")
	}
}

func TestCorrectionsDoNotRewritePriorDecisions(t *testing.T) {
	d := fixture(t)
	correction := d.Records[1]
	correction.Revision = 2
	correction.AvailableAt = d.Records[2].AvailableAt
	var o domain.Observation
	_ = json.Unmarshal([]byte(correction.Original), &o)
	o.PriceCents = 9900
	raw, _ := json.Marshal(o)
	correction.Original, correction.SHA256 = string(raw), Digest(string(raw))
	d.Records = append(d.Records, correction)
	d = seal(t, d)
	r := run(t, d, d.Records[1].AvailableAt, d.Records[3].AvailableAt)
	if r.Frames[0].AdmissionBlocked || r.Frames[0].Snapshot.State.Case.Entry.PriceCents != 10000 {
		t.Fatal("future correction leaked into earlier frame")
	}
	last := r.Frames[1]
	if !last.AdmissionBlocked || last.Snapshot.State.Case.Entry.PriceCents != 10000 || last.Snapshot.State.Case.Exit != nil {
		t.Fatal("correction rewrote economics or admitted later actions")
	}
	found := false
	for _, e := range last.Evidence {
		found = found || e.Disposition == "correction_review_required"
	}
	if !found {
		t.Fatal("correction evidence missing")
	}
	single := run(t, d, d.Records[3].AvailableAt)
	if !reflect.DeepEqual(last, single.Frames[0]) {
		t.Fatal("correction behavior depends on cutoff partition")
	}
}

func TestDataQualityBlocksOnlyWhenVisible(t *testing.T) {
	for _, quality := range []string{"quarantined", "missing"} {
		t.Run(quality, func(t *testing.T) {
			d := fixture(t)
			gap := d.Records[1]
			gap.Key, gap.Quality, gap.Reason = "zzz-quality-gap", quality, "fixture coverage gap"
			gap.Original, gap.SHA256 = "", Digest("")
			d.Records = append(d.Records, gap)
			d = seal(t, d)
			r := run(t, d, gap.AvailableAt.Add(-time.Second), gap.AvailableAt)
			if r.Frames[0].AdmissionBlocked {
				t.Fatal("future quality gap leaked")
			}
			if !r.Frames[1].AdmissionBlocked || r.Frames[1].Snapshot.State.Case.Entry != nil {
				t.Fatal("same-tick quality gap allowed an economic action")
			}
		})
	}
}

func TestDuplicatesAndInputOrder(t *testing.T) {
	d := fixture(t)
	baseline := run(t, d, d.Records[3].AvailableAt).Frames[0].Snapshot
	d.Records = append(d.Records, d.Records[1])
	slices.Reverse(d.Records)
	d = seal(t, d)
	r := run(t, d, time.Date(2026, 1, 5, 16, 0, 0, 0, time.UTC))
	if !reflect.DeepEqual(baseline, r.Frames[0].Snapshot) {
		t.Fatal("duplicates or source order changed economics")
	}
	duplicates := 0
	for _, e := range r.Frames[0].Evidence {
		if e.Disposition == "duplicate" {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatal("duplicate was not identified")
	}
}

func TestDatasetIdentityProvenanceAndReadback(t *testing.T) {
	d := fixture(t)
	raw, _ := json.Marshal(d)
	loaded, err := Read(strings.NewReader(string(raw)))
	if err != nil || !reflect.DeepEqual(d, loaded) {
		t.Fatalf("dataset round trip: %v", err)
	}
	d.Records[0].Original += " "
	if err := d.Validate(); err == nil {
		t.Fatal("changed source admitted under old content identity")
	}
	for _, name := range []string{"schema", "class", "provenance", "mapping", "times", "conflict", "raw_metadata"} {
		t.Run(name, func(t *testing.T) {
			d := fixture(t)
			switch name {
			case "schema":
				d.Schema = "v2"
			case "class":
				d.EvidenceClass = "historical"
			case "provenance":
				d.Records[0].Access.License = ""
			case "mapping":
				d.Records[0].Instrument.Version = "v2"
			case "times":
				d.Records[0].AvailableAt = d.Records[0].OccurredAt.Add(-time.Second)
			case "conflict":
				r := d.Records[0]
				r.Access.SourceRef = "different"
				d.Records = append(d.Records, r)
			case "raw_metadata":
				d.Records[0].ReceivedAt = d.Records[0].ReceivedAt.Add(time.Second)
			}
			if _, err := Seal(d); err == nil {
				t.Fatal("invalid dataset sealed")
			}
		})
	}
	if _, err := Read(strings.NewReader(string(raw) + " {}")); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestPinsClockAndCancellation(t *testing.T) {
	d := fixture(t)
	at := d.Records[3].AvailableAt
	pins := FixturePins()
	pins.RecipeVersion = "v2"
	if _, err := Run(context.Background(), d, pins, []time.Time{at}); err == nil {
		t.Fatal("incompatible recipe admitted")
	}
	for _, cutoffs := range [][]time.Time{nil, {{}}, {at, at}, {at, at.Add(-time.Second)}} {
		if _, err := Run(context.Background(), d, FixturePins(), cutoffs); err == nil {
			t.Fatal("invalid clock admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, d, FixturePins(), []time.Time{at}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
