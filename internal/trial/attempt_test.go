package trial

import (
	"strings"
	"testing"
	"time"
)

func TestAttemptIntentBoundary(t *testing.T) {
	c, p, at := fixture(t)
	r, err := Register(c, p, nil, at)
	if err != nil {
		t.Fatal(err)
	}
	command := AttemptCommand{ReceiverID: r.ReceiverID, RequestID: "intent", TrialID: r.ID}
	a, err := StartAttempt(command, r, at)
	if err != nil || a.Validate() != nil || a.ExecutionStarted || a.QualificationCredit {
		t.Fatal(a, err)
	}
	closed, err := CancelAttempt(a, at.Add(time.Second))
	if err != nil || closed.Validate() != nil || closed.IntentSHA256 != a.IntentSHA256 || closed.SHA256 == a.SHA256 {
		t.Fatal(closed, err)
	}
	for _, change := range []func(*Attempt){func(a *Attempt) { a.ExecutionStarted = true }, func(a *Attempt) { a.QualificationCredit = true }, func(a *Attempt) { a.Status = "completed" }, func(a *Attempt) { a.RegistrationSHA256 = strings.Repeat("a", 64) }, func(a *Attempt) { a.StartedAt = a.StartedAt.Add(time.Second) }} {
		copy := a
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("changed intent admitted")
		}
	}
	for _, raw := range []string{`{"receiver_id":"r","request_id":"a","trial_id":"t","result":{}}`, `{"receiver_id":"r","request_id":"a","request_id":"b","trial_id":"t"}`, `{"Receiver_ID":"r","request_id":"a","trial_id":"t"}`} {
		if _, err := ReadAttemptCommand(strings.NewReader(raw)); err == nil {
			t.Fatal("invalid command accepted")
		}
	}
	if _, err := ReadAttemptCancellation(strings.NewReader(`{"receiver_id":"r","execution_started":true}`)); err == nil {
		t.Fatal("client execution flag accepted")
	}
}
