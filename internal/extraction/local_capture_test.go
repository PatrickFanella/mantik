package extraction

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLocalCaptureImmutableReplayAndHistory(t *testing.T) {
	ctx := context.Background()
	p, f := inspectionReceipts(t, 503, []byte("private response"))
	at := time.Now().UTC()
	a, err := RetainLocal(ctx, "receiver-abcdefghijklmnop", "unknown", p, nil, at)
	if err != nil || a.Validate() != nil || a.Inspection.State != "prepared_only" || a.Inspection.Outcome != "" {
		t.Fatal(a, err)
	}
	b, err := RetainLocal(ctx, a.ReceiverID, "finished", p, &f, at)
	if err != nil || b.Validate() != nil || b.Inspection.State != "finished" || b.Inspection.Outcome != "abstain" {
		t.Fatal(b, err)
	}
	h, err := MakeLocalCaptureHistory(ctx, a.ReceiverID, []LocalCapture{a, b})
	if err != nil || h.Validate() != nil || h.CaptureCount != 2 || h.StateCounts["prepared_only"] != 1 || h.StateCounts["finished"] != 1 {
		t.Fatal(h, err)
	}
	if _, err = MakeLocalCaptureHistory(ctx, a.ReceiverID, []LocalCapture{a, a}); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	// Even a resealed outer record cannot forge a checked report or authority.
	for _, change := range []func(*LocalCapture){func(r *LocalCapture) { r.Inspection.Outcome = "extracted" }, func(r *LocalCapture) { r.Authority = "recorded_fixture" }, func(r *LocalCapture) { r.ExecutionAttested = true }, func(r *LocalCapture) { r.QualificationCredit = true }, func(r *LocalCapture) { r.Finished.Result.QualificationCredit = true }} {
		r := copyJSON(b)
		change(&r)
		r.SHA256 = ""
		r.SHA256 = hash(r)
		if r.Validate() == nil {
			t.Fatal("resealed import forgery accepted")
		}
	}
	original := copyJSON(b)
	f.Response.Base64 = "changed"
	if !reflect.DeepEqual(b, original) {
		t.Fatal("input alias mutated import")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = RetainLocal(cancelled, a.ReceiverID, "cancel", p, nil, at); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
