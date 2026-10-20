package candidateattempt

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func TestHistoryPinsMembershipAndDetachedValues(t *testing.T) {
	c, r, raw, at := fixture(t)
	first, err := Start(c, r, raw, 1, at)
	if err != nil {
		t.Fatal(err)
	}
	child := r
	child.ID = "child"
	child.Sequence = 2
	child.ParentID = r.ID
	child.ParentSHA256 = r.SHA256
	child.RegisteredAt = at.Add(time.Second)
	child.SHA256 = ""
	child.SHA256 = workerplan.Hash(child)
	unused := child
	unused.ID = "unused"
	unused.Sequence = 3
	unused.SHA256 = ""
	unused.SHA256 = workerplan.Hash(unused)
	c.RequestID = "second"
	c.RegistrationID = child.ID
	c.RegistrationSHA256 = child.SHA256
	second, err := Start(c, child, raw, 2, at.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	all := []Intent{first, second}
	regs := []candidatelineage.Registration{r, child, unused}
	h, err := HistoryFor(r.ReceiverID, regs, all)
	if err != nil || h.Validate() != nil || len(h.Registrations) != 2 || h.Registrations[0].ID != r.ID || h.Registrations[1].ID != child.ID {
		t.Fatal(h, err)
	}
	empty, err := HistoryFor(r.ReceiverID, regs, nil)
	if err != nil || empty.Intents == nil || empty.Registrations == nil || len(empty.Registrations) != 0 || empty.Validate() != nil {
		t.Fatal(empty, err)
	}
	for _, kind := range []string{"missing-pin", "extra-pin", "registration-sha", "candidate-sha", "future-registration", "pin-order", "null-intents", "null-pins", "receiver", "authority", "execution", "host", "qualified"} {
		t.Run(kind, func(t *testing.T) {
			v, _ := HistoryFor(r.ReceiverID, regs, all)
			switch kind {
			case "missing-pin":
				v.Registrations = v.Registrations[:1]
			case "extra-pin":
				v.Registrations = append(v.Registrations, RegistrationPin{ID: unused.ID, Sequence: unused.Sequence, SHA256: unused.SHA256, CandidateSHA256: unused.Candidate.SHA256, RegisteredAt: unused.RegisteredAt})
			case "registration-sha":
				v.Registrations[0].SHA256 = strings.Repeat("a", 64)
			case "candidate-sha":
				v.Registrations[0].CandidateSHA256 = strings.Repeat("b", 64)
			case "future-registration":
				v.Registrations[0].RegisteredAt = at.Add(time.Hour)
			case "pin-order":
				v.Registrations[0], v.Registrations[1] = v.Registrations[1], v.Registrations[0]
			case "null-intents":
				v.Intents = nil
			case "null-pins":
				v.Registrations = nil
			case "receiver":
				v.ReceiverID = "receiver-other_other_other"
			case "authority":
				v.Authority = "executed"
			case "execution":
				v.ExecutionVerified = true
			case "host":
				v.HostStateKnown = true
			case "qualified":
				v.QualificationCredit = true
			}
			v.SHA256 = historyHash(v)
			if v.Validate() == nil {
				t.Fatal("forged history accepted")
			}
		})
	}
	h.Intents[0].Command.Job.Plan.Cases[0].ID = "changed"
	if first.Validate() != nil {
		t.Fatal("history kept caller slices")
	}
	bad := append([]Intent(nil), all...)
	bad[0].Command.RegistrationSHA256 = strings.Repeat("a", 64)
	bad[0].SHA256 = ""
	bad[0].SHA256 = workerplan.Hash(bad[0])
	if _, err := HistoryFor(r.ReceiverID, regs, bad); err == nil {
		t.Fatal("history constructor accepted changed registration binding")
	}
	rawJSON, _ := json.Marshal(empty)
	if !strings.Contains(string(rawJSON), `"registrations":[]`) || !strings.Contains(string(rawJSON), `"intents":[]`) {
		t.Fatal("empty arrays changed transport")
	}
}
