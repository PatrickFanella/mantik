package candidategeneration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"testing"
	"time"
)

func outcomeIntents(t *testing.T, n int) (IntentHistory, []byte, time.Time) {
	t.Helper()
	p, raw, at := fixture(t)
	// This pure fixture declares publication binding; storage tests must exercise
	// publications actually issued by an authoritative research receiver.
	pub := hypothesis.Publication{Schema: hypothesis.PublicationSchema, ID: "publication", ReceiverID: "receiver-synthetic_fixture_1234", PublishedAt: at, OriginBindingVerified: true, Sealed: p.Hypothesis}
	all := []Intent{}
	for k := range n {
		c := Command{ReceiverID: pub.ReceiverID, RequestID: fmt.Sprintf("generation-%d", k+1), PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: p}
		i, err := newIntent(c, pub, k+1, at.Add(time.Duration(k+1)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, i)
	}
	h, err := IntentsFor(pub.ReceiverID, all)
	if err != nil {
		t.Fatal(err)
	}
	return h, raw, at
}
func outcomeReport(t *testing.T, i Intent, raw, output []byte) Report {
	t.Helper()
	r, err := NewRecording(i.Command.Plan, raw, output, i.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return Report{Schema: ReportSchema, ReceiverID: i.Command.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Recording: r}
}
func outcomeClone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func TestGenerationOutcomesKeepRejectedOutputAndExactIdentity(t *testing.T) {
	h, raw, _ := outcomeIntents(t, 1)
	i := h.Intents[0]
	source := "package candidate\r\nfunc Entry(p int64)bool{return p<=10000}\r\n"
	good, _ := json.Marshal(Output{Schema: OutputSchema, Source: source})
	badGo, _ := json.Marshal(Output{Schema: OutputSchema, Source: "deliberately invalid Go"})
	for _, test := range []struct {
		name      string
		output    []byte
		reason    string
		candidate bool
	}{{"valid", good, "source_payload_retained", true}, {"invalid-go", badGo, "source_payload_retained", true}, {"empty", []byte{}, "missing_output", false}, {"binary", []byte{0xff, 0, 0xfe}, "invalid_output", false}, {"partial", []byte(`{"schema":"mantik-candidate-generation-output-v1","source":`), "invalid_output", false}, {"invalid-source", []byte(`{"schema":"mantik-candidate-generation-output-v1","source":""}`), "invalid_source", false}} {
		t.Run(test.name, func(t *testing.T) {
			r := outcomeReport(t, i, raw, test.output)
			o, err := NewOutcome(i, r, r.Recording.CapturedAt.Add(time.Second))
			if err != nil || o.Validate() != nil || o.VerifyIntent(i) != nil || o.ReasonCode != test.reason || o.RecordingSHA256 != r.Recording.SHA256 || o.OutputSHA256 != workerplan.Digest(test.output) || o.OutputBytes != len(test.output) {
				t.Fatal(o, err)
			}
			if (o.CandidateSHA256 != "") != test.candidate {
				t.Fatal("candidate classification changed")
			}
			if o.ProviderCalled || o.ModelIdentityVerified || o.ModelAuthorshipVerified || o.SourceCompiled || o.SourceExecuted || o.QualificationCredit {
				t.Fatal("false authority")
			}
			a, err := NewOutcomeArtifact(i, o, r.Recording)
			if err != nil || a.Validate() != nil || !bytes.Equal(a.Recording.Output, test.output) {
				t.Fatal("artifact lost original", err)
			}
			if test.name == "valid" && a.Recording.Candidate.Source.Text != source {
				t.Fatal("source bytes changed")
			}
			encoded, _ := json.Marshal(a)
			decoded, err := ReadOutcomeArtifact(bytes.NewReader(encoded))
			if err != nil || !reflect.DeepEqual(decoded, a) {
				t.Fatal("artifact roundtrip", err)
			}
			reportRaw, _ := json.Marshal(r)
			rr, err := ReadReport(bytes.NewReader(reportRaw))
			if err != nil || !reflect.DeepEqual(rr, r) {
				t.Fatal("report roundtrip", err)
			}
			// Constructor output is detached from all caller-owned nested values.
			r.Recording.Plan.Hypothesis.Declaration.Label = "changed"
			if len(r.Recording.Output) > 0 {
				r.Recording.Output[0] ^= 1
			}
			if a.Validate() != nil {
				t.Fatal("caller mutated artifact")
			}
		})
	}
}
func TestGenerationOutcomeRetriesAndSparseHistory(t *testing.T) {
	h, raw, _ := outcomeIntents(t, MaxIntents)
	if len(h.Intents) != MaxIntents {
		t.Fatal("fixture")
	}
	missing, err := OutcomesFor(h, nil)
	if err != nil || missing.Validate() != nil || len(missing.UnreportedRequestIDs) != MaxIntents || missing.Outcomes == nil {
		t.Fatal(err)
	}
	reports := make([]Report, MaxIntents)
	all := []Outcome{}
	for n := MaxIntents - 1; n >= 0; n-- {
		r := outcomeReport(t, h.Intents[n], raw, []byte{0xff, byte(n)})
		reports[n] = r
		o, duplicate, err := AppendOutcome(h, all, r, func() time.Time { return r.Recording.CapturedAt })
		if err != nil || duplicate {
			t.Fatal(err)
		}
		all = append([]Outcome{o}, all...)
		// A subset of reports is valid; complement retains original intent order.
		partial, err := OutcomesFor(h, all)
		if err != nil || partial.Validate() != nil || len(partial.UnreportedRequestIDs) != n {
			t.Fatal("sparse history", err)
		}
	}
	full, err := OutcomesFor(h, all)
	if err != nil || len(full.Outcomes) != MaxIntents || len(full.UnreportedRequestIDs) != 0 {
		t.Fatal(err)
	}
	for n, r := range reports {
		o, duplicate, err := AppendOutcome(h, all, r, func() time.Time { panic("exact retry consulted clock") })
		if err != nil || !duplicate || !reflect.DeepEqual(o, all[n]) {
			t.Fatal("exact retry changed", err)
		}
	}
	all[0].ReasonCode = "modified"
	h.Intents[0].Command.Plan.Hypothesis.Declaration.Label = "modified"
	if full.Validate() != nil {
		t.Fatal("caller mutated history")
	}
	// One observed outcome in the middle leaves both earlier/later intents unreported.
	h, raw, _ = outcomeIntents(t, 3)
	r := outcomeReport(t, h.Intents[1], raw, []byte{})
	o, err := NewOutcome(h.Intents[1], r, r.Recording.CapturedAt)
	if err != nil {
		t.Fatal(err)
	}
	partial, err := OutcomesFor(h, []Outcome{o})
	if err != nil || !reflect.DeepEqual(partial.UnreportedRequestIDs, []string{"generation-1", "generation-3"}) {
		t.Fatal("complement changed", err)
	}
}
func TestGenerationOutcomeRefusesChangedBindingsBeforeClock(t *testing.T) {
	h, raw, _ := outcomeIntents(t, 2)
	i := h.Intents[0]
	r := outcomeReport(t, i, raw, []byte{0xff})
	o, err := NewOutcome(i, r, r.Recording.CapturedAt)
	if err != nil {
		t.Fatal(err)
	}
	noClock := func() time.Time { panic("invalid report consulted clock") }
	for _, kind := range []string{"receiver", "request", "intent-pin", "plan", "before-intent", "authority", "recording-pin", "changed-output"} {
		t.Run(kind, func(t *testing.T) {
			bad := outcomeClone(r)
			switch kind {
			case "receiver":
				bad.ReceiverID = "receiver-other_receiver_1234"
			case "request":
				bad.RequestID = "missing"
			case "intent-pin":
				bad.IntentSHA256 = strings.Repeat("f", 64)
			case "plan":
				p, err := NewPlan(i.Command.Plan.Hypothesis.Declaration, raw, i.Command.Plan.Toolchain.ImageSHA256, "other-model", i.Command.Plan.Requirement, i.Command.Plan.RequestedAt)
				if err != nil {
					t.Fatal(err)
				}
				rec, err := NewRecording(p, raw, []byte{0xff}, r.Recording.CapturedAt)
				if err != nil {
					t.Fatal(err)
				}
				bad.Recording = rec
			case "before-intent":
				bad.Recording.CapturedAt = i.CreatedAt.Add(-time.Nanosecond)
				bad.Recording.SHA256 = ""
				bad.Recording.SHA256 = workerplan.Hash(bad.Recording)
			case "authority":
				bad.Recording.ModelAuthorshipVerified = true
				bad.Recording.SHA256 = ""
				bad.Recording.SHA256 = workerplan.Hash(bad.Recording)
			case "recording-pin":
				bad.Recording.SHA256 = strings.Repeat("f", 64)
			case "changed-output":
				rec, err := NewRecording(i.Command.Plan, raw, []byte{0xfe}, r.Recording.CapturedAt)
				if err != nil {
					t.Fatal(err)
				}
				bad.Recording = rec
			}
			if _, _, err := AppendOutcome(h, []Outcome{o}, bad, noClock); err == nil {
				t.Fatal("changed binding accepted")
			}
		})
	}
	if _, err := NewOutcome(i, r, r.Recording.CapturedAt.Add(-time.Nanosecond)); err == nil {
		t.Fatal("retention predates capture")
	}
	if _, err := NewOutcome(i, r, time.Time{}); err == nil {
		t.Fatal("zero server time")
	}
	// A genuine same-plan output with an earlier claimed time still cannot bind
	// to a later service intent; portable timestamps do not prove execution time.
	before := outcomeClone(r)
	before.Recording.CapturedAt = i.Command.Plan.RequestedAt
	before.Recording.SHA256 = ""
	before.Recording.SHA256 = workerplan.Hash(before.Recording)
	if _, err := NewOutcome(i, before, r.Recording.CapturedAt); err == nil {
		t.Fatal("pre-intent recording accepted")
	}
}
func TestGenerationOutcomeSelfHashedForgeriesAndStrictReaders(t *testing.T) {
	h, raw, _ := outcomeIntents(t, 3)
	i := h.Intents[1]
	r := outcomeReport(t, i, raw, []byte{0xff})
	o, err := NewOutcome(i, r, r.Recording.CapturedAt)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewOutcomeArtifact(i, o, r.Recording)
	if err != nil {
		t.Fatal(err)
	}
	history, err := OutcomesFor(h, []Outcome{o})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"owner", "ordinal", "intent", "plan", "recording", "output", "candidate", "status", "reason", "time", "flag", "record"} {
		t.Run(kind, func(t *testing.T) {
			v := outcomeClone(a)
			switch kind {
			case "owner":
				v.Outcome.ReceiverID = "receiver-other_receiver_1234"
			case "ordinal":
				v.Outcome.IntentSequence = 1
			case "intent":
				v.Outcome.IntentSHA256 = strings.Repeat("f", 64)
			case "plan":
				v.Outcome.PlanSHA256 = strings.Repeat("f", 64)
			case "recording":
				v.Outcome.RecordingSHA256 = strings.Repeat("f", 64)
			case "output":
				v.Outcome.OutputSHA256 = strings.Repeat("f", 64)
			case "candidate":
				v.Outcome.CandidateSHA256 = strings.Repeat("f", 64)
			case "status":
				v.Outcome.Status = "candidate_recorded"
			case "reason":
				v.Outcome.ReasonCode = "missing_output"
			case "time":
				v.Outcome.CapturedAt = i.CreatedAt.Add(-time.Nanosecond)
			case "flag":
				v.Outcome.ProviderCalled = true
			case "record":
				v.Recording.Output = []byte{0xfe}
				v.Recording.OutputSHA256 = workerplan.Digest(v.Recording.Output)
				v.Recording.SHA256 = ""
				v.Recording.SHA256 = workerplan.Hash(v.Recording)
			}
			v.Outcome.SHA256 = ""
			v.Outcome.SHA256 = workerplan.Hash(v.Outcome)
			if v.Validate() == nil {
				t.Fatal("forgery accepted")
			}
		})
	}
	for _, kind := range []string{"null-outcomes", "null-complement", "omit-unreported", "extra-unreported", "reordered-complement", "duplicate-outcome", "bad-ordinal", "wrong-intent", "wrong-owner", "qualified", "provider", "authorship"} {
		t.Run(kind, func(t *testing.T) {
			v := outcomeClone(history)
			switch kind {
			case "null-outcomes":
				v.Outcomes = nil
			case "null-complement":
				v.UnreportedRequestIDs = nil
			case "omit-unreported":
				v.UnreportedRequestIDs = v.UnreportedRequestIDs[:1]
			case "extra-unreported":
				v.UnreportedRequestIDs = append(v.UnreportedRequestIDs, "generation-2")
			case "reordered-complement":
				v.UnreportedRequestIDs[0], v.UnreportedRequestIDs[1] = v.UnreportedRequestIDs[1], v.UnreportedRequestIDs[0]
			case "duplicate-outcome":
				v.Outcomes = append(v.Outcomes, v.Outcomes[0])
			case "bad-ordinal":
				v.Outcomes[0].IntentSequence = 0
				v.Outcomes[0].SHA256 = ""
				v.Outcomes[0].SHA256 = workerplan.Hash(v.Outcomes[0])
			case "wrong-intent":
				v.Outcomes[0].IntentSHA256 = strings.Repeat("f", 64)
				v.Outcomes[0].SHA256 = ""
				v.Outcomes[0].SHA256 = workerplan.Hash(v.Outcomes[0])
			case "wrong-owner":
				v.ReceiverID = "receiver-other_receiver_1234"
			case "qualified":
				v.QualificationCredit = true
			case "provider":
				v.ProviderCalled = true
			case "authorship":
				v.ModelAuthorshipVerified = true
			}
			v.SHA256 = ""
			v.SHA256 = workerplan.Hash(v)
			if v.Validate() == nil {
				t.Fatal("history forgery accepted")
			}
		})
	}
	for _, artifact := range []bool{false, true} {
		good, _ := json.Marshal(r)
		limit := MaxReportBytes
		if artifact {
			good, _ = json.Marshal(a)
			limit = MaxOutcomeArtifactBytes
		}
		for _, bad := range [][]byte{[]byte("null"), append(bytes.Clone(good), []byte("{}")...), []byte(strings.Replace(string(good), `"schema":`, `"schema":"duplicate","schema":`, 1)), []byte(strings.Replace(string(good), `"schema":`, `"Schema":`, 1)), []byte(strings.Replace(string(good), `"schema":`, `"extra":true,"schema":`, 1)), bytes.Repeat([]byte(" "), limit+1)} {
			if artifact {
				if _, err := ReadOutcomeArtifact(bytes.NewReader(bad)); err == nil {
					t.Fatal("ambiguous artifact accepted")
				}
			} else {
				if _, err := ReadReport(bytes.NewReader(bad)); err == nil {
					t.Fatal("ambiguous report accepted")
				}
			}
		}
	}
}
