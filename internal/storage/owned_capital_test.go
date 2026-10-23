package storage

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func assertOwnedCapital(t *testing.T, r OwnedCapitalReadback, a paperaccount.Account, source string) {
	t.Helper()
	hash := r.SHA256
	r.SHA256 = ""
	if r.Schema != "mantik-owned-deferral-capital-v1" || r.ReceiverID != a.ReceiverID || !reflect.DeepEqual(r.Account, a) || r.Source != source || r.AvailableCashCents != a.Config.OpeningCashCents || r.RealizedPnLCents != 0 || r.Positions == nil || len(r.Positions) != 0 || r.Orders == nil || len(r.Orders) != 0 || r.Commitments == nil || len(r.Commitments) != 0 || r.ExecutionAvailable || r.QualificationCredit || r.Authority != "service_owned_deferral_capital_readback_only" || hash != workerplan.Hash(r) {
		t.Fatal("invalid capital readback", r)
	}
}

func TestOwnedCapitalSourceValidationAndOrdering(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	for _, id := range []string{"z-account", "a-account"} {
		a := policyAccount(t, m, id)
		c := policyCommand(a, "policy-"+id)
		if _, err := m.RegisterAllocationPolicy(ctx, c, func() time.Time { return time.Unix(200, 0).UTC() }); err != nil {
			t.Fatal(err)
		}
	}
	policies, err := m.AllocationPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := clone(policies)
	rows, err := ownedCapitalReadbacks(ctx, policies.ReceiverID, policies, nil)
	if err != nil || len(rows) != 2 || rows[0].Account.Config.ID != "a-account" || rows[1].Account.Config.ID != "z-account" {
		t.Fatal(rows, err)
	}
	for _, r := range rows {
		assertOwnedCapital(t, r, r.Account, "registered_opening_capital")
	}
	rows[0].Account.Config.OpeningCashCents = 1
	if !reflect.DeepEqual(before, policies) {
		t.Fatal("readback mutated retained policy")
	}
	ledger, err := allocation.NewLedger(policies.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	// A valid pure-core reservation is still only an owner-supplied assertion.
	// This service readback must not present it as this deferral owner's state.
	at := time.Unix(201, 0).UTC()
	rule := ledger.Policy.Command.Policy.Strategies[0]
	funded, _, _, e := allocation.AppendLedger(ctx, ledger, allocation.LedgerCommand{ID: "asserted-reservation", Comparison: &allocation.Comparison{Proposals: []allocation.Proposal{{ID: "asserted", DecisionSHA256: strings.Repeat("b", 64), StrategyID: rule.ID, Symbol: "DEMO", Origin: "research", NotionalCents: 20000, ValidUntil: at.Add(time.Minute)}}, Admissions: []allocation.Admission{{StrategyID: rule.ID, ArtifactSHA256: rule.ArtifactSHA256, EvidenceSHA256: strings.Repeat("c", 64), ValidFrom: at.Add(-time.Second), ValidUntil: at.Add(time.Hour)}}}}, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	for name, ls := range map[string][]allocation.Ledger{
		"duplicate":        {ledger, ledger},
		"funded_assertion": {funded},
		"empty_history":    {ledger},
		"invalid":          {func() allocation.Ledger { l := clone(ledger); l.SHA256 = "invalid"; return l }()},
		"orphan":           {ledger},
	} {
		t.Run(name, func(t *testing.T) {
			ps := clone(policies)
			if name == "orphan" {
				ps.Records = []allocation.PolicyRecord{}
			}
			result, e := ownedCapitalReadbacks(ctx, ps.ReceiverID, ps, ls)
			if e == nil || result != nil {
				t.Fatal("invalid source returned capital", result, e)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	result, err := ownedCapitalReadbacks(canceled, policies.ReceiverID, policies, nil)
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatal(result, err)
	}
}
