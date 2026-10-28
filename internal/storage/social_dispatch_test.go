package storage

import (
	"context"
	"encoding/json"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/socialwatch"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"github.com/jackc/pgx/v5/pgxpool"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemorySocialDispatch(t *testing.T) { socialDispatchContract(t, NewMemory()) }
func TestPostgresContractsSocialDispatch(t *testing.T) {
	socialDispatchContract(t, timerPostgres(t, "socialdispatch"))
}
func socialDispatchContract(t *testing.T, s interface {
	SocialWatchStore
	SocialResearchDispatchStore
}) {
	ctx := context.Background()
	h, err := s.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	now := func() time.Time { return at }
	p := socialPolicy(h.ReceiverID)
	policy, err := s.ApplySocialCommand(ctx, socialwatch.Command{Policy: &p}, now)
	if err != nil {
		t.Fatal(err)
	}
	c := socialCapture(h.ReceiverID, policy.Entry.Policy.SHA256, at)
	c.RequestResearch = true
	cap, err := s.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, now)
	if err != nil {
		t.Fatal(err)
	}
	request := cap.Entry.Capture.ResearchRequest
	command := SocialResearchDispatchCommand{ReceiverID: h.ReceiverID, RequestID: request.ID, RequestSHA256: request.SHA256}
	first, err := s.DispatchSocialResearch(ctx, command, now)
	if err != nil || len(first.Record.Links) != 1 || first.Record.AdmissionAvailable || first.Record.ProviderAccessVerified {
		t.Fatal(first, err)
	}
	retry, err := s.DispatchSocialResearch(ctx, command, func() time.Time { panic("retry sampled clock") })
	if err != nil || !retry.Duplicate || retry.Record.SHA256 != first.Record.SHA256 {
		t.Fatal(retry, err)
	}
	all, err := s.SocialResearchDispatches(ctx)
	if err != nil || len(all.Records) != 1 {
		t.Fatal(all, err)
	}
	command.RequestSHA256 = first.Record.SHA256
	if _, err = s.DispatchSocialResearch(ctx, command, now); err == nil {
		t.Fatal("changed request accepted")
	}
}

func TestSocialDispatchRejectsStaleAndReplayTampering(t *testing.T) {
	m := NewMemory()
	ctx := t.Context()
	h, _ := m.SocialHistory(ctx)
	at := time.Now().UTC()
	p := socialPolicy(h.ReceiverID)
	policy, err := m.ApplySocialCommand(ctx, socialwatch.Command{Policy: &p}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	c := socialCapture(h.ReceiverID, policy.Entry.Policy.SHA256, at)
	c.RequestResearch = true
	cap, err := m.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	req := cap.Entry.Capture.ResearchRequest
	cmd := SocialResearchDispatchCommand{ReceiverID: h.ReceiverID, RequestID: req.ID, RequestSHA256: req.SHA256}
	if _, err = m.DispatchSocialResearch(ctx, cmd, func() time.Time { return at.Add(61 * time.Second) }); err == nil {
		t.Fatal("stale assertion dispatched")
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, e := m.DispatchSocialResearch(ctx, cmd, func() time.Time { calls.Add(1); return at }); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	saved := clone(m.state.Routing)
	originalJournal := clone(*m.socialJournal)
	for _, kind := range []string{"marker", "case", "input", "head"} {
		m.state.Routing = clone(saved)
		switch kind {
		case "marker":
			m.state.Routing.SocialDispatches = nil
		case "case":
			for i := range m.state.Routing.Cases {
				if m.state.Routing.Cases[i].Owner == routing.OwnedSocialResearchSource {
					m.state.Routing.Cases[i].Behavior = "act"
				}
			}
		case "input":
			for id, v := range m.state.Routing.Inputs {
				if v.Input.Source == routing.OwnedSocialResearchSource {
					v.Input.EvidenceClass = "prospective_market_unqualified"
					m.state.Routing.Inputs[id] = v
				}
			}
		case "head":
			v := &m.state.Routing.SocialDispatches[0]
			v.SocialHeadSHA256 = workerplan.Digest([]byte("missing"))
			v.SHA256 = ""
			v.SHA256 = workerplan.Hash(*v)
		}
		if _, e := m.SocialResearchDispatches(ctx); e == nil {
			t.Fatal("accepted tamper", kind)
		}
	}
	m.state.Routing = clone(saved)
	o, _ := c.Decode()
	o.Kind = "delete"
	o.Revision = 2
	o.Text = ""
	o.Symbols = []string{}
	o.OccurredAt = at.Add(time.Second)
	o.ObservedAt = o.OccurredAt
	raw, _ := json.Marshal(o)
	c.ID = "deletion"
	c.RequestResearch = false
	c.Original = string(raw)
	c.OriginalSHA256 = workerplan.Digest(raw)
	if _, err = m.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, func() time.Time { return at.Add(time.Second) }); err != nil {
		t.Fatal(err)
	}
	if v, e := m.DispatchSocialResearch(ctx, cmd, func() time.Time { panic("retry clock") }); e != nil || !v.Duplicate {
		t.Fatal(v, e)
	}
	m.state.Routing = routing.NewState()
	if _, err = m.DispatchSocialResearch(ctx, cmd, func() time.Time { return at.Add(time.Second) }); err == nil {
		t.Fatal("deleted source opened new case")
	}
	m.socialJournal = &originalJournal
	m.state.Routing = clone(saved)
	link := saved.SocialDispatches[0].Links[0]
	i := saved.Inputs[link.InputID].Input
	if _, err = m.RouteInput(ctx, i); err == nil {
		t.Fatal("caller injected reserved source")
	}
	if _, err = m.RegisterTrigger(ctx, domain.TriggerDefinition{ID: "social-owned-forgery", Owner: routing.OwnedSocialResearchSource}); err == nil {
		t.Fatal("caller registered reserved owner")
	}
}

func TestPostgresContractsSocialDispatchCommitRecovery(t *testing.T) {
	p := timerPostgres(t, "socialdispatchcommit")
	ctx := t.Context()
	h, err := p.SocialHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	now := func() time.Time { return at }
	policy := socialPolicy(h.ReceiverID)
	pr, err := p.ApplySocialCommand(ctx, socialwatch.Command{Policy: &policy}, now)
	if err != nil {
		t.Fatal(err)
	}
	c := socialCapture(h.ReceiverID, pr.Entry.Policy.SHA256, at)
	c.RequestResearch = true
	cr, err := p.ApplySocialCommand(ctx, socialwatch.Command{Capture: &c}, now)
	if err != nil {
		t.Fatal(err)
	}
	req := cr.Entry.Capture.ResearchRequest
	cmd := SocialResearchDispatchCommand{ReceiverID: h.ReceiverID, RequestID: req.ID, RequestSHA256: req.SHA256}
	before, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(ctx, `CREATE FUNCTION reject_dispatch_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'manufactured dispatch commit rejection'; END $$; CREATE CONSTRAINT TRIGGER reject_dispatch_commit AFTER UPDATE ON mantik_state DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_dispatch_commit()`); err != nil {
		t.Fatal(err)
	}
	got, err := p.DispatchSocialResearch(ctx, cmd, now)
	if _, drop := p.pool.Exec(ctx, "DROP TRIGGER reject_dispatch_commit ON mantik_state; DROP FUNCTION reject_dispatch_commit()"); drop != nil {
		t.Fatal(drop)
	}
	if err == nil || !reflect.DeepEqual(got, SocialResearchDispatchReceipt{}) {
		t.Fatal("failed COMMIT acknowledged dispatch", got, err)
	}
	after, err := p.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("rejected commit changed state", err)
	}
	catalog, err := p.SocialResearchDispatches(ctx)
	if err != nil || len(catalog.Records) != 0 {
		t.Fatal(catalog, err)
	}
	first, err := p.DispatchSocialResearch(ctx, cmd, now)
	if err != nil || first.Duplicate {
		t.Fatal(first, err)
	}
	reopened, err := OpenPostgres(ctx, p.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	retry, err := reopened.DispatchSocialResearch(ctx, cmd, func() time.Time { panic("restart retry sampled clock") })
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(first.Record, retry.Record) {
		t.Fatal(retry, err)
	}
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	tracer := &cancelCollectorCommit{cancel: cancel}
	config := p.pool.Config()
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uncertain, err := (&Postgres{pool: pool}).DispatchSocialResearch(request, cmd, func() time.Time { panic("retry clock") })
	if !tracer.fired.Load() || err == nil || !reflect.DeepEqual(uncertain, SocialResearchDispatchReceipt{}) {
		t.Fatal("canceled COMMIT acknowledged dispatch", uncertain, err)
	}
	final, err := reopened.SocialResearchDispatches(ctx)
	if err != nil || len(final.Records) != 1 || final.Records[0].SHA256 != first.Record.SHA256 {
		t.Fatal(final, err)
	}
}
