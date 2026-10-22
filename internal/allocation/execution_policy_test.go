package allocation

import (
	"bytes"
	"encoding/json"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/paperfill"
)

func TestPolicyExecutionModelPinsAndLegacy(t *testing.T) {
	p, s, at := fixture(t)
	c := PolicyCommand{ReceiverID: s.Account.ReceiverID, AccountID: s.Account.Config.ID, Policy: p}
	legacy, err := NewPolicyRecord(c, s.Account, at)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(legacy)
	if bytes.Contains(raw, []byte("execution_model")) || legacy.Schema != "mantik-owned-allocation-policy-v1" {
		t.Fatal(string(raw))
	}
	model := paperfill.Model{Method: paperfill.Method, LiquidityBPS: 10000, MaxQuoteAgeSeconds: 60}
	c.ExecutionModel = &model
	owned, err := NewPolicyRecord(c, s.Account, at)
	if err != nil || owned.Schema != "mantik-owned-allocation-policy-v2" || owned.SHA256 == legacy.SHA256 || owned.Validate() != nil || owned.ReservationsAvailable || owned.QualificationCredit {
		t.Fatal(owned, err)
	}
	model.FeeBPS = 10
	if owned.Command.ExecutionModel.FeeBPS != 0 {
		t.Fatal("caller mutation")
	}
	changed, err := NewPolicyRecord(c, s.Account, at)
	if err != nil || changed.SHA256 == owned.SHA256 {
		t.Fatal(changed, err)
	}
	owned.Command.ExecutionModel.FeeBPS++
	if owned.Validate() == nil {
		t.Fatal("changed model accepted")
	}
	model.Method = "unknown"
	if _, err = NewPolicyRecord(c, s.Account, at); err == nil {
		t.Fatal("unknown method accepted")
	}
	if legacy.Validate() != nil {
		t.Fatal("legacy changed")
	}
}
