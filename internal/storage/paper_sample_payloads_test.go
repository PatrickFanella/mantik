package storage

import (
	"bytes"
	"encoding/json"
	"maps"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

func TestMemoryPaperSamplePayloads(t *testing.T) {
	paperSamplePayloadContract(t, NewMemory())
}

func TestPostgresContractsPaperSamplePayloads(t *testing.T) {
	paperSamplePayloadContract(t, timerPostgres(t, "papersamplepayloads"))
}

func paperSamplePayloadContract(t *testing.T, s paperArchiveBackend) {
	t.Helper()
	ctx := t.Context()
	plan, _ := declarationFixture(t, s, "prospective_market_unqualified")
	at := plan.Plan.WindowStart
	now := func() time.Time { return at }
	reg := feedcollector.Registration{ID: "payload-feed", Provider: "alpaca", Feed: "iex", Symbol: "SPY", EvidenceClass: "prospective_market_unqualified", JournalID: "journal-one", AccessRef: "fixture-only", LeaseSeconds: 300}
	for _, c := range []feedcollector.Command{{RequestID: "register", Operation: "register", RegistrationID: reg.ID, Registration: &reg}, {RequestID: "claim", Operation: "claim", RegistrationID: reg.ID, OwnerID: "worker"}} {
		if _, err := s.FeedCollectorCommand(ctx, plan.ReceiverID, c, now); err != nil {
			t.Fatal(err)
		}
	}
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		return PaperDecisionEvaluation{Action: "abstain", ReasonCode: "payload_fixture_only"}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		return PaperOutcomeEvaluation{Status: "not_applicable", ReasonCode: "payload_fixture_only"}, nil
	}
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
			t.Fatal(err)
		}
		originals[kind+":"+id] = raw
	}
	decision := func(id string, epoch int64) (PaperDecisionRecord, paperSampleRecord) {
		t.Helper()
		at = at.Add(time.Nanosecond)
		r, err := s.FinalizePaperDecision(ctx, PaperDecisionCommand{ID: id, ReceiverID: plan.ReceiverID, StrategyID: plan.Plan.Variants[0].ID, StrategySHA256: plan.Plan.Variants[0].SHA256, RegistrationID: reg.ID}, now, de)
		if err != nil {
			t.Fatal(err)
		}
		remember("decision", id)
		return r.Record, paperSampleRecord{PaperRecordEpoch{"decision", id, epoch, r.Record.SHA256, r.Record.FinalizedAt}, paperScopeForDecision(r.Record), 0}
	}
	d, original := decision("original", 0)
	at = at.Add(time.Nanosecond)
	o, err := s.AssessPaperOutcome(ctx, PaperOutcomeCommand{ID: "outcome", ReceiverID: plan.ReceiverID, DecisionID: d.Command.ID, DecisionSHA256: d.SHA256}, now, de, oe)
	if err != nil {
		t.Fatal(err)
	}
	remember("outcome", "outcome")
	outcome := paperSampleRecord{PaperRecordEpoch{"outcome", "outcome", 0, o.Record.SHA256, o.Record.EvaluatedAt}, paperScopeForDecision(d), 0}
	seal := func() {
		t.Helper()
		at = at.Add(time.Nanosecond)
		if _, err := s.ArchivePaperFeed(ctx, plan.ReceiverID, now, func(FeedEvidenceSnapshot, time.Time) error { return nil }, de, oe); err != nil {
			t.Fatal(err)
		}
		at = at.Add(time.Nanosecond)
		if _, err := s.ArchivePaperRecords(ctx, plan.ReceiverID, now, de, oe); err != nil {
			t.Fatal(err)
		}
	}
	seal()
	_, later := decision("later", 1)
	seal() // A valid second archive also contains the original first payloads.
	var corrupted retainedPaperDecision
	if err := json.Unmarshal(originals["decision:original"], &corrupted); err != nil {
		t.Fatal(err)
	}
	corrupted.Basis.Feed.Collectors.LastClock = corrupted.Basis.Feed.Collectors.LastClock.Add(time.Nanosecond)
	corruptRaw, _ := json.Marshal(corrupted)

	type sqlExec func(string, ...any)
	check := func(name string, wanted []paperSampleRecord, damageMemory func(*Memory), damageSQL func(sqlExec), fails bool) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			var got paperArchiveOwners
			var err error
			var read func() (paperArchiveOwners, error)
			if m, ok := s.(*Memory); ok {
				m.mu.Lock()
				defer m.mu.Unlock()
				decisions, outcomes := maps.Clone(m.paperDecisions), maps.Clone(m.paperOutcomes)
				retired, scopes := maps.Clone(m.paperRecordRetiredEpochs), maps.Clone(m.paperRecordScopes)
				archives := append([]PaperRecordArchive{}, m.paperRecordArchives...)
				defer func() {
					m.paperDecisions, m.paperOutcomes, m.paperRecordRetiredEpochs, m.paperRecordScopes, m.paperRecordArchives = decisions, outcomes, retired, scopes, archives
				}()
				if m.paperRecordRetiredEpochs == nil {
					m.paperRecordRetiredEpochs = map[string]int64{}
				}
				if damageMemory != nil {
					damageMemory(m)
				}
				read = func() (paperArchiveOwners, error) { return m.paperSamplePayloadsLocked(ctx, plan.ReceiverID, wanted) }
			} else {
				tx, e := s.(*Postgres).pool.Begin(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer tx.Rollback(ctx)
				if damageSQL != nil {
					damageSQL(func(sql string, args ...any) {
						if _, e := tx.Exec(ctx, sql, args...); e != nil {
							t.Fatal("damage fixture did not reach resolver", e)
						}
					})
				}
				read = func() (paperArchiveOwners, error) { return readPaperSamplePayloads(ctx, tx, plan.ReceiverID, wanted) }
			}
			got, err = read()
			if fails {
				if err == nil || !reflect.DeepEqual(got, paperArchiveOwners{}) {
					t.Fatal("damaged sample returned partial or complete owners", err)
				}
				return
			}
			if err != nil || len(got.decisions)+len(got.outcomes) != len(wanted) {
				t.Fatal("sample custody", err)
			}
			for _, w := range wanted {
				var raw []byte
				if w.Kind == "decision" {
					raw, err = json.Marshal(got.decisions[w.ID])
				} else {
					raw, err = json.Marshal(got.outcomes[w.ID])
				}
				if err != nil || !bytes.Equal(raw, originals[w.Kind+":"+w.ID]) {
					t.Fatal("original sample envelope changed", err)
				}
			}
			for id, d := range got.decisions {
				if _, present := d.Basis.Feed.Collectors.Entries[reg.ID]; !present {
					t.Fatal("detachment fixture lost original collector")
				}
				delete(d.Basis.Feed.Collectors.Entries, reg.ID)
				again, e := read()
				raw, _ := json.Marshal(again.decisions[id])
				if e != nil || !bytes.Equal(raw, originals["decision:"+id]) {
					t.Fatal("returned sample basis aliases retained state", e)
				}
				break
			}
		})
	}
	check("active", []paperSampleRecord{original, outcome, later}, nil, nil, false)
	check("missing-unretired-even-with-archives", []paperSampleRecord{original}, func(m *Memory) { delete(m.paperDecisions, "original") }, func(exec sqlExec) { exec("DELETE FROM mantik_paper_decisions WHERE id='original'") }, true)
	check("changed-active-never-falls-back", []paperSampleRecord{original}, func(m *Memory) { m.paperDecisions["original"] = corrupted }, func(exec sqlExec) {
		exec("UPDATE mantik_paper_decisions SET payload=$1 WHERE id='original'", corruptRaw)
	}, true)
	forged := original
	forged.RetiredEpoch = 2
	check("caller-cannot-authorize-retirement", []paperSampleRecord{forged}, nil, nil, true)
	check("duplicate-page", []paperSampleRecord{original, original}, nil, nil, true)
	unknown := original
	unknown.ID, unknown.Scope.DecisionID = "absent", "absent"
	check("missing-identity", []paperSampleRecord{unknown}, nil, nil, true)
	retiredOriginal, retiredOutcome := original, outcome
	retiredOriginal.RetiredEpoch, retiredOutcome.RetiredEpoch = 2, 2
	retireMemory := func(m *Memory) {
		m.paperRecordRetiredEpochs["decision:original"], m.paperRecordRetiredEpochs["outcome:outcome"] = 2, 2
		delete(m.paperDecisions, "original")
		delete(m.paperOutcomes, "outcome")
	}
	retireSQL := func(exec sqlExec) {
		exec("ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE record_id IN ('original','outcome'); ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_paper_outcomes WHERE id='outcome'; DELETE FROM mantik_paper_decisions WHERE id='original'")
	}
	check("approved-originals", []paperSampleRecord{retiredOriginal, retiredOutcome, later}, retireMemory, retireSQL, false)
	check("missing-first-original-never-uses-later-copy", []paperSampleRecord{retiredOriginal}, func(m *Memory) { retireMemory(m); m.paperRecordArchives[0] = PaperRecordArchive{} }, func(exec sqlExec) {
		retireSQL(exec)
		exec("ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives")
		exec("UPDATE mantik_paper_record_archives SET payload=$1 WHERE sequence=1", []byte(`{}`))
	}, true)
	retiredLater := later
	retiredLater.RetiredEpoch = 2
	check("later-failure-erases-earlier-owners", []paperSampleRecord{outcome, retiredLater}, func(m *Memory) {
		m.paperRecordRetiredEpochs["decision:later"] = 2
		delete(m.paperDecisions, "later")
		m.paperRecordArchives[1] = PaperRecordArchive{}
	}, func(exec sqlExec) {
		exec("ALTER TABLE mantik_paper_record_epochs DISABLE TRIGGER mantik_immutable_paper_epochs; UPDATE mantik_paper_record_epochs SET retired_epoch=2 WHERE record_id='later'; ALTER TABLE mantik_paper_record_epochs ENABLE TRIGGER mantik_immutable_paper_epochs; DELETE FROM mantik_paper_decisions WHERE id='later'; ALTER TABLE mantik_paper_record_archives DISABLE TRIGGER mantik_immutable_paper_archives")
		exec("UPDATE mantik_paper_record_archives SET payload=$1 WHERE sequence=2", []byte(`{}`))
	}, true)
}
