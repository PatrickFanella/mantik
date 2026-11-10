package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
)

// Selection fixtures pin the original field locations independently of SQL.
// Shared owner contracts separately establish actual native segment replay.
func financialSelectionCases() []struct {
	name string
	item retainedOwnedAllocation
	want []string
} {
	effect := func(id, kind string) allocation.LedgerEntry {
		return allocation.LedgerEntry{Command: allocation.LedgerCommand{Effect: &allocation.Effect{DecisionID: id, Kind: kind}}}
	}
	return []struct {
		name string
		item retainedOwnedAllocation
		want []string
	}{
		{"comparison-and-other-decisions-effects", retainedOwnedAllocation{Record: OwnedAllocationRecord{
			Command:     OwnedAllocationCommand{Decisions: []OwnedDecisionPin{{ID: "decision-b"}}},
			Expirations: []allocation.LedgerEntry{effect("decision-a", "release")},
			Executions: []OwnedExecutionAdvance{{Entries: []allocation.LedgerEntry{
				effect("decision-a", "close"), effect("decision-c", "fill"), {},
			}}},
		}}, []string{"decision-a", "decision-b", "decision-c"}},
		{"standalone-execution-and-null-record", retainedOwnedAllocation{Execution: &OwnedExecutionRecord{
			Executions: []OwnedExecutionAdvance{{Entries: []allocation.LedgerEntry{
				effect("decision-a", "fill"), effect("decision-a", "close"), effect("decision-a", "release"),
			}}},
		}}, []string{"decision-a"}},
		{"legacy-comparison", retainedOwnedAllocation{Record: OwnedAllocationRecord{
			Command: OwnedAllocationCommand{Decisions: []OwnedDecisionPin{{ID: "decision-a"}}},
		}}, []string{"decision-a"}},
		{"no-reference", retainedOwnedAllocation{}, []string{}},
	}
}

func TestAllocationFinancialDecisionSources(t *testing.T) {
	for _, c := range financialSelectionCases() {
		got, err := allocationFinancialDecisions(c.item)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatal(c.name, got, c.want, err)
		}
	}
	if _, err := allocationFinancialDecisions(retainedOwnedAllocation{Record: OwnedAllocationRecord{Command: OwnedAllocationCommand{Decisions: []OwnedDecisionPin{{ID: "../another-owner"}}}}}); !errors.Is(err, ErrOwnedAllocationInvalid) {
		t.Fatal("invalid original decision reference accepted", err)
	}
}

func TestPostgresContractsAllocationFinancialDecisionSources(t *testing.T) {
	p := timerPostgres(t, "allocationfinancialsources")
	for _, c := range financialSelectionCases() {
		raw, err := json.Marshal(c.item)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := p.pool.Query(t.Context(), "SELECT decision_id FROM mantik_allocation_financial_decisions($1::jsonb)", string(raw))
		if err != nil {
			t.Fatal(c.name, err)
		}
		got := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			got = append(got, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatal("SQL lost an original financial decision", c.name, got, c.want, err)
		}
	}
}
