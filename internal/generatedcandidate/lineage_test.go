package generatedcandidate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/candidategeneration"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (Command, hypothesis.Publication, candidategeneration.OutcomeArtifact, []byte, time.Time) {
	t.Helper()
	d, _ := dataset.FixtureDataset()
	raw, _ := json.Marshal(d)
	raw = append(raw, '\n')
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h, err := hypothesis.Seal(hypothesis.Fixture())
	if err != nil {
		t.Fatal(err)
	}
	// Pure fixtures declare publication ownership; backend tests must issue it
	// through the actual receiver and validate every retained dependency.
	pub := hypothesis.Publication{Schema: hypothesis.PublicationSchema, ID: "publication", ReceiverID: "receiver-synthetic_fixture_1234", PublishedAt: at, OriginBindingVerified: true, Sealed: h}
	plan, err := candidategeneration.NewPlan(h.Declaration, raw, "sha256:"+strings.Repeat("a", 64), "gpt-6.1-sol", "Propose a synthetic threshold.", at)
	if err != nil {
		t.Fatal(err)
	}
	i, _, err := candidategeneration.AppendIntent(nil, candidategeneration.Command{ReceiverID: pub.ReceiverID, RequestID: "generation", PublicationID: pub.ID, PublicationSHA256: workerplan.Hash(pub), Plan: plan}, pub, raw, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	output, _ := json.Marshal(candidategeneration.Output{Schema: candidategeneration.OutputSchema, Source: "invalid Go deliberately retained for later compile failure\r\n"})
	rec, err := candidategeneration.NewRecording(plan, raw, output, at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	report := candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: pub.ReceiverID, RequestID: i.Command.RequestID, IntentSHA256: i.SHA256, Recording: rec}
	o, err := candidategeneration.NewOutcome(i, report, at.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	a, err := candidategeneration.NewOutcomeArtifact(i, o, rec)
	if err != nil {
		t.Fatal(err)
	}
	c := Command{ReceiverID: pub.ReceiverID, RequestID: "registered", GenerationRequestID: i.Command.RequestID, IntentSHA256: i.SHA256, OutcomeSHA256: o.SHA256, RecordingSHA256: rec.SHA256, PlanSHA256: plan.SHA256, CandidateSHA256: rec.Candidate.SHA256}
	return c, pub, a, raw, at.Add(4 * time.Second)
}
func TestGeneratedRegistrationPreservesLegacyAndExactSource(t *testing.T) {
	c, p, a, raw, at := fixture(t)
	l, r, duplicate, err := Append(nil, nil, c, p, a, raw, func() time.Time { return at })
	if err != nil || duplicate || l.Validate() != nil || l.VerifyRegistration(r) != nil {
		t.Fatal(l, err)
	}
	legacy, err := candidatelineage.Register(candidatelineage.Command{ReceiverID: c.ReceiverID, RequestID: c.RequestID, PublicationID: p.ID, Candidate: *a.Recording.Candidate}, p, nil, 1, at)
	if err != nil || !reflect.DeepEqual(legacy, r) {
		t.Fatal("legacy registration wire/hash changed", err)
	}
	if r.Candidate.Source.Text != a.Recording.Candidate.Source.Text || r.Candidate.SourceCompiled || r.Candidate.SourceExecuted || r.ExecutionStarted || r.QualificationCredit || l.ProviderCalled || l.ModelAuthorshipVerified || l.SourceCompiled || l.SourceExecuted || l.QualificationCredit {
		t.Fatal("changed source/authority")
	}
	artifact, err := NewArtifact(l, r, a)
	if err != nil || artifact.Validate() != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(artifact)
	read, err := ReadArtifact(bytes.NewReader(encoded))
	if err != nil || !reflect.DeepEqual(read, artifact) {
		t.Fatal("artifact read changed", err)
	}
	commandRaw, _ := json.Marshal(c)
	readCommand, err := ReadCommand(bytes.NewReader(commandRaw))
	if err != nil || readCommand != c {
		t.Fatal("command read changed", err)
	}
	a.Recording.Output[0] ^= 1
	a.Recording.Plan.Hypothesis.Declaration.Label = "changed"
	r.Candidate.Hypothesis.Declaration.Label = "changed"
	if artifact.Validate() != nil {
		t.Fatal("artifact aliases caller")
	}
}
func TestGeneratedRegistrationCapacityParentsAndClockFreeRetries(t *testing.T) {
	c, p, a, raw, at := fixture(t)
	history := []candidatelineage.Registration{}
	links := []Link{}
	for n := 1; n <= candidatelineage.MaxRegistrations; n++ {
		next := c
		next.RequestID = fmt.Sprintf("registration-%02d", n)
		if n > 1 {
			next.ParentID = history[n-2].ID
		}
		l, r, duplicate, err := Append(history, links, next, p, a, raw, func() time.Time { return at.Add(time.Duration(n) * time.Second) })
		if err != nil || duplicate || r.Sequence != n {
			t.Fatal(n, err)
		}
		if n > 1 && r.ParentSHA256 != history[n-2].SHA256 {
			t.Fatal("parent pin changed")
		}
		history = append(history, r)
		links = append(links, l)
	}
	candidates, _ := candidatelineage.RegistryFor(c.ReceiverID, history)
	registry, err := RegistryFor(candidates, links)
	if err != nil || registry.Validate() != nil || len(registry.UnlinkedRegistrationIDs) != 0 || len(registry.Links) != candidatelineage.MaxRegistrations {
		t.Fatal(err)
	}
	for n := range links {
		l, r, duplicate, err := Append(history, links, links[n].Command, p, a, raw, func() time.Time { panic("full retry read clock") })
		if err != nil || !duplicate || !reflect.DeepEqual(l, links[n]) || !reflect.DeepEqual(r, history[n]) {
			t.Fatal("retry changed", n, err)
		}
	}
	extra := c
	extra.RequestID = "overflow"
	if _, _, _, err := Append(history, links, extra, p, a, raw, func() time.Time { panic("capacity read clock") }); !errors.Is(err, candidatelineage.ErrCapacity) {
		t.Fatal("capacity overflow", err)
	}
	links[0].Command.PlanSHA256 = strings.Repeat("f", 64)
	history[0].Candidate.Hypothesis.Declaration.Label = "caller changed"
	if registry.Validate() != nil {
		t.Fatal("registry aliases caller")
	}
}
func TestGeneratedRegistrationRefusesRetrofitAndChangedBindings(t *testing.T) {
	c, p, a, raw, at := fixture(t)
	l, r, _, err := Append(nil, nil, c, p, a, raw, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	noClock := func() time.Time { panic("invalid binding read clock") }
	if _, _, _, err := Append([]candidatelineage.Registration{r}, nil, c, p, a, raw, noClock); err == nil {
		t.Fatal("unlinked registration adopted")
	}
	manual, _ := candidatelineage.RegistryFor(c.ReceiverID, []candidatelineage.Registration{r})
	unlinked, err := RegistryFor(manual, nil)
	if err != nil || !reflect.DeepEqual(unlinked.UnlinkedRegistrationIDs, []string{r.ID}) {
		t.Fatal("manual registration discovery changed", err)
	}
	child := c
	child.RequestID = "child"
	child.ParentID = r.ID
	cl, cr, _, err := Append([]candidatelineage.Registration{r}, nil, child, p, a, raw, func() time.Time { return at.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	mixed, _ := candidatelineage.RegistryFor(c.ReceiverID, []candidatelineage.Registration{r, cr})
	reg, err := RegistryFor(mixed, []Link{cl})
	if err != nil || !reflect.DeepEqual(reg.UnlinkedRegistrationIDs, []string{r.ID}) {
		t.Fatal("sparse links changed", err)
	}
	for _, kind := range []string{"receiver", "generation-request", "intent", "outcome", "recording", "plan", "candidate", "parent", "publication", "dataset", "source", "rejected"} {
		t.Run(kind, func(t *testing.T) {
			bad := c
			bp := detach(p)
			ba := detach(a)
			br := bytes.Clone(raw)
			switch kind {
			case "receiver":
				bad.ReceiverID = "receiver-other_other_other"
			case "generation-request":
				bad.GenerationRequestID = "other"
			case "intent":
				bad.IntentSHA256 = strings.Repeat("f", 64)
			case "outcome":
				bad.OutcomeSHA256 = strings.Repeat("f", 64)
			case "recording":
				bad.RecordingSHA256 = strings.Repeat("f", 64)
			case "plan":
				bad.PlanSHA256 = strings.Repeat("f", 64)
			case "candidate":
				bad.CandidateSHA256 = strings.Repeat("f", 64)
			case "parent":
				bad.ParentID = "missing"
			case "publication":
				bp.PublishedAt = bp.PublishedAt.Add(time.Nanosecond)
			case "dataset":
				br = append(br, ' ')
			case "source":
				ba.Recording.Candidate.Source.Text = "changed"
			case "rejected":
				rec, err := candidategeneration.NewRecording(a.Intent.Command.Plan, raw, []byte{}, a.Recording.CapturedAt)
				if err != nil {
					t.Fatal(err)
				}
				report := candidategeneration.Report{Schema: candidategeneration.ReportSchema, ReceiverID: c.ReceiverID, RequestID: a.Intent.Command.RequestID, IntentSHA256: a.Intent.SHA256, Recording: rec}
				o, err := candidategeneration.NewOutcome(a.Intent, report, a.Outcome.RecordedAt)
				if err != nil {
					t.Fatal(err)
				}
				ba, err = candidategeneration.NewOutcomeArtifact(a.Intent, o, rec)
				if err != nil {
					t.Fatal(err)
				}
				bad.OutcomeSHA256 = o.SHA256
				bad.RecordingSHA256 = rec.SHA256
			}
			if _, _, _, err := Append([]candidatelineage.Registration{r}, []Link{l}, bad, bp, ba, br, noClock); err == nil {
				t.Fatal("changed linked retry accepted", kind)
			}
		})
	}
	fresh := c
	fresh.RequestID = "early"
	if _, reg, _, err := Append(nil, nil, fresh, p, a, raw, func() time.Time { return a.Outcome.RecordedAt.Add(-time.Nanosecond) }); err == nil || reg.ID != "" {
		t.Fatal("backdated registration acknowledged", err)
	}
}
func TestGeneratedRegistrationRejectsSelfHashedForgeriesAndStrictReaders(t *testing.T) {
	c, p, a, raw, at := fixture(t)
	l, r, _, err := Append(nil, nil, c, p, a, raw, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := NewArtifact(l, r, a)
	if err != nil {
		t.Fatal(err)
	}
	candidates, _ := candidatelineage.RegistryFor(c.ReceiverID, []candidatelineage.Registration{r})
	registry, err := RegistryFor(candidates, []Link{l})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"registration", "ordinal", "publication", "registered-time", "outcome-time", "authorship", "qualification", "source", "generation", "output"} {
		t.Run(kind, func(t *testing.T) {
			v := detach(artifact)
			switch kind {
			case "registration":
				v.Link.RegistrationSHA256 = strings.Repeat("f", 64)
			case "ordinal":
				v.Link.RegistrationSequence = 2
			case "publication":
				v.Link.PublicationSHA256 = strings.Repeat("f", 64)
			case "registered-time":
				v.Link.RegisteredAt = v.Link.RegisteredAt.Add(time.Nanosecond)
			case "outcome-time":
				v.Link.OutcomeRecordedAt = v.Link.OutcomeRecordedAt.Add(-time.Nanosecond)
			case "authorship":
				v.Link.ModelAuthorshipVerified = true
			case "qualification":
				v.Link.QualificationCredit = true
			case "source":
				v.Registration.Candidate.Source.Text = "different source"
			case "generation":
				v.Generation.Intent.Command.RequestID = "other"
			case "output":
				v.Generation.Recording.Output = []byte{0xff}
			}
			v.Link.SHA256 = ""
			v.Link.SHA256 = workerplan.Hash(v.Link)
			if v.Validate() == nil {
				t.Fatal("forged artifact accepted")
			}
		})
	}
	for _, kind := range []string{"null-links", "null-complement", "false-complement", "duplicate-link", "ordinal-zero", "wrong-owner", "authorship", "qualification"} {
		t.Run(kind, func(t *testing.T) {
			v := detach(registry)
			switch kind {
			case "null-links":
				v.Links = nil
			case "null-complement":
				v.UnlinkedRegistrationIDs = nil
			case "false-complement":
				v.UnlinkedRegistrationIDs = []string{r.ID}
			case "duplicate-link":
				v.Links = append(v.Links, l)
			case "ordinal-zero":
				v.Links[0].RegistrationSequence = 0
				v.Links[0].SHA256 = ""
				v.Links[0].SHA256 = workerplan.Hash(v.Links[0])
			case "wrong-owner":
				v.ReceiverID = "receiver-other_other_other"
			case "authorship":
				v.ModelAuthorshipVerified = true
			case "qualification":
				v.QualificationCredit = true
			}
			v.SHA256 = ""
			v.SHA256 = workerplan.Hash(v)
			if v.Validate() == nil {
				t.Fatal("forged registry accepted")
			}
		})
	}
	for _, isArtifact := range []bool{false, true} {
		good, _ := json.Marshal(c)
		limit := MaxCommandBytes
		field := `"receiver_id":`
		if isArtifact {
			good, _ = json.Marshal(artifact)
			limit = MaxArtifactBytes
			field = `"schema":`
		}
		for _, bad := range [][]byte{[]byte("null"), []byte(strings.Replace(string(good), `"receiver_id":`, `"Receiver_id":`, 1)), append(bytes.Clone(good), []byte("{}")...), []byte(strings.Replace(string(good), field, `"extra":true,`+field, 1)), []byte(strings.Replace(string(good), field, field+`"duplicate",`+field, 1)), bytes.Repeat([]byte(" "), limit+1)} {
			if isArtifact {
				if _, err := ReadArtifact(bytes.NewReader(bad)); err == nil {
					t.Fatal("ambiguous artifact accepted")
				}
			} else {
				if _, err := ReadCommand(bytes.NewReader(bad)); err == nil {
					t.Fatal("ambiguous command accepted")
				}
			}
		}
	}
}
