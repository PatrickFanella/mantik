package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
)

func TestMLegOpeningReservesInternalClientIDNamespace(t *testing.T) {
	for _, id := range []string{
		"mleg-lifecycle", "mleg-close-plan", "mleg-quarantine",
		"mleg-observation-00000001", "mleg-reply-pending-example",
		"mleg-refused-example", "mleg-close-example", "mleg-leg-example-0",
		"mleg-future-control",
	} {
		t.Run(id, func(t *testing.T) {
			v, p, _ := newFakeMlegVenue(t)
			o := mlegFixtureOpen()
			o.Order.ClientOrderID = id
			admitted := false
			if _, err := p.OpenMLeg(context.Background(), o, func() error { admitted = true; return nil }); err == nil {
				t.Error("reserved client ID accepted")
			}
			if len(v.posts) != 0 || v.deletes != 0 || admitted {
				t.Error("reserved client ID reached admission or external mutation", len(v.posts), v.deletes, admitted)
			}
			names, err := directoryNames(p.journal.root)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range names {
				if name == optionLifecycle || name == optionIntentName(id) {
					t.Error("reserved client ID retained a conflicting lifecycle/intent", name)
				}
			}
			if _, err := p.journal.read(optionLifecycle, MaxBytes); !errors.Is(err, os.ErrNotExist) {
				t.Error("refusal consumed the lifecycle", err)
			}
			// Refusal before custody leaves the journal usable with a safe ID.
			testOpenMleg(t, p, mlegFixtureOpen())
		})
	}
}

func TestMLegRetainedPrefixedClientIDStillRecoversAndCloses(t *testing.T) {
	v, p, root := newFakeMlegVenue(t)
	o := mlegFixtureOpen()
	o.Order.ClientOrderID = "mleg-existing-open"
	request := optionRequest{MLegOrder: o.Order}
	for name, value := range map[string]any{optionLifecycle: o, optionIntentName(o.Order.ClientOrderID): request} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.journal.writeOnce(name, raw); err != nil {
			t.Fatal(err)
		}
	}
	v.orders[o.Order.ClientOrderID] = mlegResponse(request, 2, "filled")
	v.requests[o.Order.ClientOrderID] = request
	restartOptionJournal(t, p, root)
	if _, err := p.OpenMLeg(context.Background(), o, nil); err != nil || len(v.posts) != 0 {
		t.Fatal("retained opening did not recover by GET only", err, v.posts)
	}
	plan := mlegPlan()
	plan.OpenClientOrderID = o.Order.ClientOrderID
	out, err := p.CloseMLeg(context.Background(), plan)
	if err != nil || out.State != "closed" || len(v.posts) != 1 || v.posts[0].ClientOrderID != optionCloseID(o.Order.ClientOrderID) {
		t.Fatal("namespace restriction stranded retained exposure", out, err, v.posts)
	}
}
