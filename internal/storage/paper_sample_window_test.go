package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"github.com/jackc/pgx/v5"
)

func TestMemoryPaperSampleWindow(t *testing.T) {
	paperSampleWindowContract(t, NewMemory())
}

func TestPostgresContractsPaperSampleWindow(t *testing.T) {
	paperSampleWindowContract(t, timerPostgres(t, "papersamplewindow"))
}

func paperSampleWindowContract(t *testing.T, s paperArchiveBackend) {
	t.Helper()
	ctx := t.Context()
	command, base := declarationFixture(t, s, "prospective_market_unqualified")
	variants := append([]PaperEvaluationVariant{}, command.Plan.Variants...)
	command.Plan.Variants = command.Plan.Variants[:1]
	command.Plan.WindowEnd = command.Plan.WindowStart.Add(time.Microsecond)
	declaration, err := s.DeclarePaperEvaluation(ctx, command, func() time.Time { return base.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	at := command.Plan.WindowStart.Add(-20 * time.Nanosecond)
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "window-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, command.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "sample_window_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "sample_window_fixture_only"}, nil
	}
	want := map[string]paperSampleRecord{}
	originals := map[string][]byte{}
	remember := func(kind, id string) {
		t.Helper()
		var raw []byte
		var err error
		if m, ok := s.(*Memory); ok {
			if kind == "decision" {
				raw, err = json.Marshal(m.paperDecisions[id])
			} else {
				raw, err = json.Marshal(m.paperOutcomes[id])
			}
		} else {
			table := "mantik_paper_decisions"
			if kind == "outcome" {
				table = "mantik_paper_outcomes"
			}
			err = s.(*Postgres).pool.QueryRow(ctx, "SELECT payload FROM "+table+" WHERE id=$1", id).Scan(&raw)
		}
		if err != nil {
			t.Fatal("original sample envelope", err)
		}
		originals[kind+":"+id] = raw
	}
	addDecision := func(id string, variant int, ns int64, epoch int64, selected bool) PaperDecisionRecord {
		t.Helper()
		at = command.Plan.WindowStart.Add(time.Duration(ns))
		v := variants[variant]
		r, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: id, ReceiverID: command.ReceiverID, StrategyID: v.ID, StrategySHA256: v.SHA256, RegistrationID: reg.ID}, now, de)
		if err != nil {
			t.Fatal("finalize window fixture", id, err)
		}
		if selected {
			want["decision:"+id] = paperSampleRecord{PaperRecordEpoch{"decision", id, epoch, r.Record.SHA256, r.Record.FinalizedAt}, paperScopeForDecision(r.Record), 0}
			remember("decision", id)
		}
		return r.Record
	}
	addOutcome := func(id string, d PaperDecisionRecord, epoch int64) {
		t.Helper()
		r, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: id, ReceiverID: command.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256}, now, de, oe)
		if err != nil {
			t.Fatal("outcome window fixture", id, err)
		}
		want["outcome:"+id] = paperSampleRecord{PaperRecordEpoch{"outcome", id, epoch, r.Record.SHA256, r.Record.EvaluatedAt}, paperScopeForDecision(d), 0}
		remember("outcome", id)
	}
	addDecision("before-window", 0, -1, 0, false)
	for i := range 64 {
		d := addDecision(fmt.Sprintf("old-%03d", i), 0, int64(i*2), 0, true)
		at = at.Add(time.Nanosecond)
		addOutcome(fmt.Sprintf("old-outcome-%03d", i), d, 0)
	}
	at = command.Plan.WindowStart.Add(130 * time.Nanosecond)
	if _, err := s.ArchivePaperFeed(ctx, command.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Nanosecond)
	archive, err := s.ArchivePaperRecords(ctx, command.ReceiverID, now, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	// Production retirement is still forbidden. This disposable fixture tests
	// the future atomic move, with real owner envelopes and original custody.
	if m, ok := s.(*Memory); ok {
		m.paperRecordRetiredEpochs = map[string]int64{}
		for key, e := range m.paperRecordEpochs {
			if e.Kind == "decision" || e.Kind == "outcome" {
				m.paperRecordRetiredEpochs[key] = 1
			}
		}
		clear(m.paperDecisions)
		clear(m.paperOutcomes)
	} else {
		if _, err := s.(*Postgres).pool.Exec(ctx, "ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET retired_epoch=1 WHERE kind IN ('decision','outcome'); ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_paper_outcomes; DELETE FROM mantik_paper_decisions"); err != nil {
			t.Fatal(err)
		}
	}
	for key, r := range want {
		r.RetiredEpoch = 1
		want[key] = r
	}
	var first PaperDecisionRecord
	for i := range 224 {
		id := fmt.Sprintf("active-%03d", i)
		if i < 3 {
			id = []string{"tie-A", "tie_a", "tie-a"}[i]
		}
		d := addDecision(id, 0, int64(140+i/3), 1, true)
		if i == 0 {
			first = d
		}
	}
	for i := range 31 {
		addDecision(fmt.Sprintf("unselected-%03d", i), 1, int64(500+i), 1, false)
	}
	addDecision("exclusive-window-end", 0, 1000, 1, false)
	// Selection retains a late outcome for inspection; evaluation decides
	// applicability. The exact as-of cut determines when it is available.
	at = command.Plan.OutcomeDeadline.Add(time.Nanosecond)
	addOutcome("late-outcome", first, 1)

	var page func(PaperEvaluationDeclaration, time.Time, *paperSampleCursor, int) (paperSamplePage, error)
	var payloads func([]paperSampleRecord) (paperArchiveOwners, error)
	var native func([]paperSampleRecord) (paperArchiveOwners, error)
	if m, ok := s.(*Memory); ok {
		m.mu.Lock()
		defer m.mu.Unlock()
		page = func(d PaperEvaluationDeclaration, at time.Time, c *paperSampleCursor, n int) (paperSamplePage, error) {
			return m.paperSampleWindowLocked(ctx, d, at, c, n)
		}
		payloads = func(records []paperSampleRecord) (paperArchiveOwners, error) {
			return m.paperSamplePayloadsLocked(ctx, command.ReceiverID, records)
		}
		native = func(records []paperSampleRecord) (paperArchiveOwners, error) {
			return m.paperSampleNativeLocked(ctx, command.ReceiverID, records, de, oe)
		}
	} else {
		pg := s.(*Postgres)
		tx, err := pg.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		page = func(d PaperEvaluationDeclaration, at time.Time, c *paperSampleCursor, n int) (paperSamplePage, error) {
			return readPaperSampleWindow(ctx, tx, d, at, c, n)
		}
		payloads = func(records []paperSampleRecord) (paperArchiveOwners, error) {
			return readPaperSamplePayloads(ctx, tx, command.ReceiverID, records)
		}
		native = func(records []paperSampleRecord) (paperArchiveOwners, error) {
			return readPaperSampleNative(ctx, tx, command.ReceiverID, records, de, oe)
		}
	}
	for _, asOf := range []time.Time{command.Plan.WindowStart, command.Plan.OutcomeDeadline, at} {
		seen, previous := map[string]bool{}, ""
		var cursor *paperSampleCursor
		for batches := 0; ; batches++ {
			limit := []int{1, 17, 128}[batches%3]
			got, err := page(declaration.Record, asOf, cursor, limit)
			if err != nil || len(got.Records) > limit || got.Records == nil || batches > 1000 {
				t.Fatal("window page", batches, err)
			}
			resolved, err := payloads(got.Records)
			if err != nil || len(resolved.decisions)+len(resolved.outcomes) != len(got.Records) {
				t.Fatal("original sample payload page", err)
			}
			for _, r := range got.Records {
				key := r.Kind + ":" + r.ID
				var raw []byte
				if r.Kind == "decision" {
					raw, err = json.Marshal(resolved.decisions[r.ID])
				} else {
					raw, err = json.Marshal(resolved.outcomes[r.ID])
				}
				if err != nil || !bytes.Equal(raw, originals[key]) {
					t.Fatal("sample resolution rewrote an original envelope", key, err)
				}
				order := fmt.Sprintf("%020d.%09d\x00%s\x00%s\x00%020d.%09d\x00%s", r.Scope.DecisionAt.Unix(), r.Scope.DecisionAt.Nanosecond(), r.Scope.DecisionID, r.Kind, r.At.Unix(), r.At.Nanosecond(), r.ID)
				if seen[key] || r != want[key] || r.At.After(asOf) || order <= previous {
					t.Fatal("omission, duplication, clock or byte order changed", key)
				}
				seen[key], previous = true, order
			}
			if got.Next == nil {
				break
			}
			cursor = got.Next
		}
		count := 0
		for key, r := range want {
			if !r.At.After(asOf) {
				count++
				if !seen[key] {
					t.Fatal("page omitted an original matching owner", key)
				}
			}
		}
		if len(seen) != count {
			t.Fatal("window count", len(seen), count)
		}
	}
	// Native replay crosses the active catalogue's capacity and page boundaries
	// without substituting selected-page counts for the original checkpoints.
	seen := map[string]bool{}
	var cursor *paperSampleCursor
	for {
		batch, err := page(declaration.Record, at, cursor, maxPaperNativePage)
		if err != nil {
			t.Fatal(err)
		}
		owners, err := native(batch.Records)
		if err != nil {
			t.Fatal("large native sample page", err)
		}
		for _, r := range batch.Records {
			key := r.Kind + ":" + r.ID
			var raw []byte
			if r.Kind == "decision" {
				raw, err = json.Marshal(owners.decisions[r.ID])
			} else {
				raw, err = json.Marshal(owners.outcomes[r.ID])
			}
			if err != nil || seen[key] || !bytes.Equal(raw, originals[key]) {
				t.Fatal("native sample changed or repeated an original", key, err)
			}
			seen[key] = true
		}
		if batch.Next == nil {
			break
		}
		cursor = batch.Next
	}
	if len(seen) != len(want) {
		t.Fatal("native pages omitted sample owners", len(seen), len(want))
	}
	got, err := page(declaration.Record, at, nil, 1)
	if err != nil || got.Next == nil || len(want) != 353 {
		t.Fatal("large history fixture or initial cursor", len(want), err)
	}
	if _, err := page(declaration.Record, at.Add(time.Nanosecond), got.Next, 17); !errors.Is(err, ErrPaperRecordArchiveInvalid) {
		t.Fatal("cursor crossed an as-of snapshot", err)
	}
	other := clone(declaration.Record)
	other.Command.ReceiverID = "receiver-" + strings.Repeat("b", 32)
	if _, err := page(other, at, nil, 17); !errors.Is(err, ErrReceiverMismatch) {
		t.Fatal("foreign declaration selected local owner history", err)
	}
	other = clone(declaration.Record)
	other.Command.Plan.Variants = variants[1:]
	if _, err := page(other, at, got.Next, 17); !errors.Is(err, ErrPaperRecordArchiveInvalid) {
		t.Fatal("cursor crossed a declaration scope", err)
	}
	if _, err := paperSampleQuery(declaration.Record, at, 2, got.Next, 17); !errors.Is(err, ErrPaperRecordArchiveInvalid) {
		t.Fatal("cursor crossed archive heads", err)
	}
	for _, limit := range []int{0, 129} {
		if _, err := page(declaration.Record, at, nil, limit); !errors.Is(err, ErrPaperRecordArchiveInvalid) {
			t.Fatal("unbounded page accepted", limit, err)
		}
	}
	got.Records[0].Scope.StrategyID = "caller-change"
	if next, err := page(declaration.Record, at, nil, 1); err != nil || reflect.DeepEqual(next.Records, got.Records) {
		t.Fatal("page metadata aliases owner state", err)
	}
	if archive.Header.Sequence != 1 || len(want) <= MaxPaperDecisions {
		t.Fatal("fixture did not cross active capacity")
	}
	if pg, ok := s.(*Postgres); ok {
		var definition string
		if err := pg.pool.QueryRow(ctx, "SELECT indexdef FROM pg_indexes WHERE indexname='mantik_paper_sample_window'").Scan(&definition); err != nil || !strings.Contains(definition, `COLLATE "C"`) {
			t.Fatal("window index lost native byte order", definition, err)
		}
	}
}

func TestMemoryPaperSampleWindowCancellation(t *testing.T) {
	m := NewMemory()
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.paperSampleWindowLocked(c, PaperEvaluationDeclaration{}, time.Time{}, nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled empty window returned success", err)
	}
}
