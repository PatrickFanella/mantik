package candidateattempt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/candidateexec"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatelineage"
	"git.subcult.tv/subculture-collective/mantik/internal/candidatetest"
	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (Command, candidatelineage.Registration, []byte, time.Time) {
	t.Helper()
	d, err := dataset.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	p, err := candidate.New([]byte("invalid Go; compilation failure must remain possible\n"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 5, 17, 0, 0, 0, time.UTC)
	pub := hypothesis.Publication{Schema: hypothesis.PublicationSchema, ID: "publication", ReceiverID: "receiver-abcdefghijklmnop", PublishedAt: at, OriginBindingVerified: true, Sealed: p.Hypothesis}
	r, err := candidatelineage.Append(nil, candidatelineage.Command{ReceiverID: pub.ReceiverID, RequestID: "registered", PublicationID: pub.ID, Candidate: p}, pub, at)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := candidatetest.New(p, raw, []bool{true, true, false, false})
	if err != nil {
		t.Fatal(err)
	}
	j, err := candidateexec.New(plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	return Command{ReceiverID: r.ReceiverID, RequestID: "first", RegistrationID: r.ID, RegistrationSHA256: r.SHA256, Job: j}, r, raw, at
}

func TestReadExportedIntentStrictly(t *testing.T) {
	c, r, raw, at := fixture(t)
	i, err := Start(c, r, raw, 1, at)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(i)
	got, err := ReadIntent(bytes.NewReader(encoded))
	if err != nil || !reflect.DeepEqual(i, got) {
		t.Fatal(got, err)
	}
	for _, bad := range [][]byte{
		append(append([]byte{}, encoded...), []byte("{}")...),
		[]byte(strings.Replace(string(encoded), `"schema":`, `"unknown":true,"schema":`, 1)),
		[]byte(strings.Replace(string(encoded), `"schema":`, `"schema":"duplicate","schema":`, 1)),
		[]byte(strings.Replace(string(encoded), `"schema":`, `"Schema":`, 1)),
		[]byte(strings.Replace(string(encoded), `"host_state_known":false`, `"host_state_known":true`, 1)),
		bytes.Repeat([]byte(" "), MaxBytes+1),
	} {
		if _, err := ReadIntent(bytes.NewReader(bad)); !errors.Is(err, ErrInvalid) {
			t.Fatal("ambiguous or unbounded receipt accepted", err)
		}
	}
}

func TestIntentExactBindingsRetriesAndDistinctAttempts(t *testing.T) {
	c, r, raw, at := fixture(t)
	now := func() time.Time { return at }
	i, err := Append(nil, c, r, raw, now)
	if err != nil || i.Validate() != nil || i.VerifyBinding(r) != nil || i.ExecutionStarted || i.HostStateKnown || i.QualificationCredit {
		t.Fatal(i, err)
	}
	read, err := Append([]Intent{i}, c, r, raw, func() time.Time { panic("retry consulted clock") })
	if err != nil || !reflect.DeepEqual(i, read) {
		t.Fatal("retry changed", err)
	}
	c.RequestID = "physical-retry"
	second, err := Append([]Intent{i}, c, r, raw, now)
	if err != nil || second.Sequence != 2 || second.SHA256 == i.SHA256 || second.Command.Job.SHA256 != i.Command.Job.SHA256 {
		t.Fatal(second, err)
	}
	if ValidateHistory(c.ReceiverID, []Intent{i, second}) != nil {
		t.Fatal("valid history rejected")
	}
	c.Job.Plan.Cases[0].ID = "changed"
	read.Command.Job.Plan.Candidate.Hypothesis.Declaration.Methods[0].ID = "changed"
	if i.Validate() != nil || second.Validate() != nil {
		t.Fatal("caller retained nested slices")
	}
}

func TestIntentRejectsSubstitutedPinsAndRehashedCases(t *testing.T) {
	for _, kind := range []string{"receiver", "registration", "registration-sha", "candidate", "dataset", "clock", "resource-profile", "forged-case", "sequence"} {
		t.Run(kind, func(t *testing.T) {
			c, r, raw, at := fixture(t)
			seq := 1
			switch kind {
			case "receiver":
				c.ReceiverID = "receiver-other_other_other"
			case "registration":
				c.RegistrationID = "other"
			case "registration-sha":
				c.RegistrationSHA256 = strings.Repeat("b", 64)
			case "candidate":
				r.Candidate.Source.Text += "changed"
			case "dataset":
				raw = append(raw, '\n')
			case "clock":
				at = at.Add(-time.Second)
			case "sequence":
				seq = 65
			case "resource-profile":
				c.Job.Resources.PIDs++
			case "forged-case":
				c.Job.Plan.Cases[0].PriceCents++
				p := c.Job.Plan
				p.ID, p.SHA256 = "", ""
				p.SHA256 = workerplan.Hash(p)
				p.ID = "candidate-test-" + p.SHA256
				c.Job.Plan = p
				c.Job.HarnessSHA256 = workerplan.Digest([]byte(candidateexec.Harness(p)))
				c.Job.SHA256 = ""
				c.Job.SHA256 = workerplan.Hash(c.Job)
				if c.Validate() != nil {
					t.Fatal("fixture must retain valid self-hashes")
				}
			}
			if _, err := Start(c, r, raw, seq, at); err == nil {
				t.Fatal("substitution accepted")
			}
		})
	}
}

func TestIntentCapacityFullPrefixAndChangedRetry(t *testing.T) {
	c, r, raw, at := fixture(t)
	var all []Intent
	for n := range MaxAttempts {
		c.RequestID = fmt.Sprintf("attempt-%02d", n)
		i, err := Append(all, c, r, raw, func() time.Time { return at.Add(time.Duration(n) * time.Second) })
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, i)
	}
	if ValidateHistory(c.ReceiverID, all) != nil {
		t.Fatal("capacity history rejected")
	}
	if _, err := Append(all, c, r, raw, func() time.Time { panic("capacity retry consulted clock") }); err != nil {
		t.Fatal(err)
	}
	c.RequestID = "overflow"
	if _, err := Append(all, c, r, raw, func() time.Time { panic("capacity consulted clock") }); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	for _, kind := range []string{"gap", "duplicate", "clock", "authority", "host", "execution", "qualified", "receiver"} {
		t.Run(kind, func(t *testing.T) {
			encoded, _ := json.Marshal(all)
			var changed []Intent
			_ = json.Unmarshal(encoded, &changed)
			switch kind {
			case "gap":
				changed[1].Sequence = 3
			case "duplicate":
				changed[1].Command.RequestID = changed[0].Command.RequestID
			case "clock":
				changed[1].CreatedAt = at.Add(-time.Second)
			case "authority":
				changed[1].Authority = "executed"
			case "host":
				changed[1].HostStateKnown = true
			case "execution":
				changed[1].ExecutionStarted = true
			case "qualified":
				changed[1].QualificationCredit = true
			case "receiver":
				changed[1].Command.ReceiverID = "receiver-other_other_other"
			}
			changed[1].SHA256 = hash(changed[1])
			if ValidateHistory(c.ReceiverID, changed) == nil {
				t.Fatal("forged prefix accepted")
			}
			copy := all[0].Command
			if _, err := Append(changed, copy, r, raw, func() time.Time { panic("corrupt history consulted clock") }); err == nil {
				t.Fatal("retry hid corruption")
			}
		})
	}
	c = all[0].Command
	c.RegistrationSHA256 = strings.Repeat("a", 64)
	if _, err := Append(all, c, r, raw, func() time.Time { panic("changed retry consulted clock") }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c = all[0].Command
	plan, err := candidatetest.New(r.Candidate, raw, []bool{false, false, false, false})
	if err != nil {
		t.Fatal(err)
	}
	c.Job, err = candidateexec.New(plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Append(all, c, r, raw, func() time.Time { panic("annotation retry consulted clock") }); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestIntentStrictCommandRead(t *testing.T) {
	c, _, _, _ := fixture(t)
	raw, _ := json.Marshal(c)
	read, err := Read(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(c, read) {
		t.Fatal(err)
	}
	for _, bad := range []string{string(raw) + "{}", strings.Replace(string(raw), `"receiver_id":`, `"Receiver_id":`, 1), strings.Replace(string(raw), `"receiver_id":`, `"receiver_id":"duplicate","receiver_id":`, 1), strings.Replace(string(raw), `"job":`, `"execution_started":true,"job":`, 1), strings.Repeat(" ", MaxBytes+1)} {
		if _, err := Read(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous/oversized command accepted")
		}
	}
}
