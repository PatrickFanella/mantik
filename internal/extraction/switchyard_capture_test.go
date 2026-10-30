package extraction

import (
	"context"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestSwitchyardCaptureImmutableReplayAndHistory(t *testing.T) {
	ctx := context.Background()
	p, f := switchyardCaptureReceipts(t)
	at := time.Now().UTC()
	a, err := RetainSwitchyard(ctx, "receiver-abcdefghijklmnop", "unknown", p, nil, at)
	if err != nil || a.Validate() != nil || a.Inspection.State != "prepared_only" || a.Inspection.Outcome != "" {
		t.Fatal(a, err)
	}
	b, err := RetainSwitchyard(ctx, a.ReceiverID, "finished", p, &f, at)
	if err != nil || b.Validate() != nil || b.Inspection.State != "finished" || b.Inspection.Outcome != "abstain" {
		t.Fatal(b, err)
	}
	h, err := MakeSwitchyardCaptureHistory(ctx, a.ReceiverID, []SwitchyardCapture{a, b})
	if err != nil || h.Validate() != nil || h.CaptureCount != 2 || h.StateCounts["prepared_only"] != 1 || h.StateCounts["finished"] != 1 {
		t.Fatal(h, err)
	}
	if _, err = MakeSwitchyardCaptureHistory(ctx, a.ReceiverID, []SwitchyardCapture{a, a}); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	// Even a resealed outer record cannot forge a checked report or authority.
	for _, change := range []func(*SwitchyardCapture){func(r *SwitchyardCapture) { r.Inspection.Outcome = "extracted" }, func(r *SwitchyardCapture) { r.Authority = "recorded_fixture" }, func(r *SwitchyardCapture) { r.ExecutionAttested = true }, func(r *SwitchyardCapture) { r.QualificationCredit = true }, func(r *SwitchyardCapture) { r.Finished.Result.QualificationCredit = true }} {
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
	if _, err = RetainSwitchyard(cancelled, a.ReceiverID, "cancel", p, nil, at); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func switchyardCaptureReceipts(t *testing.T) (SwitchyardReceipt, SwitchyardReceipt) {
	t.Helper()
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("retained failure"))
	})))
	defer server.Close()
	var receipts []SwitchyardReceipt
	_, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", SwitchyardConfig{Endpoint: server.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r SwitchyardReceipt) error { receipts = append(receipts, r); return nil })
	if err != nil || len(receipts) != 2 {
		t.Fatal(err)
	}
	return receipts[0], receipts[1]
}
func TestSwitchyardCaptureProviderSeparation(t *testing.T) {
	ctx := context.Background()
	at := time.Now().UTC()
	receiver := "receiver-abcdefghijklmnop"
	p, f := switchyardCaptureReceipts(t)
	lp, lf := LocalReceipt(p), LocalReceipt(f)
	if _, err := RetainLocal(ctx, receiver, "wrong", lp, &lf, at); err == nil {
		t.Fatal("Switchyard capture entered local ledger")
	}
	localP, localF := inspectionReceipts(t, 503, []byte("failure"))
	sp, sf := SwitchyardReceipt(localP), SwitchyardReceipt(localF)
	if _, err := RetainSwitchyard(ctx, receiver, "wrong", sp, &sf, at); err == nil {
		t.Fatal("local capture entered Switchyard ledger")
	}
	a, err := RetainSwitchyard(ctx, receiver, "one", p, &f, at)
	if err != nil {
		t.Fatal(err)
	}
	h, err := MakeSwitchyardCaptureHistory(ctx, receiver, []SwitchyardCapture{a})
	if err != nil {
		t.Fatal(err)
	}
	h.Captures[0].Inspection.Schema = LocalInspectionSchema
	h.Captures[0].Inspection.SHA256 = ""
	h.Captures[0].Inspection.SHA256 = hash(h.Captures[0].Inspection)
	h.SHA256 = ""
	h.SHA256 = hash(h)
	if h.Validate() == nil {
		t.Fatal("resealed wrong-provider inspection accepted")
	}
}
