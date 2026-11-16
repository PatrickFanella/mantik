package service

import (
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
)

func TestReconciliationFenceCountsArchivedObservations(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	state, err := account.NewCappedState(account.DefaultCappedConfig("checkpoint", "paper", account.Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4100 {
		state, err = account.ApplyCappedCommand(state, account.BookCommand{ID: fmt.Sprint(i), Kind: "risk_observe", At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.Version != 4100 || len(state.Events) != 2 {
		t.Fatal("missing checkpoint", state.Version, len(state.Events))
	}
	// A baseline can lie inside the folded run; only observations followed it.
	rec := &incubationReconciliation{version: 2048}
	if !reconciliationUnchanged(state, rec, "allocation") {
		t.Fatal("observation-only suffix invalidated baseline")
	}
	for _, event := range []account.BookCommand{
		{ID: "other-order", Kind: "portfolio_reserve", Order: &account.BookOrder{AllocationID: "other-allocation"}},
		{ID: "fill", Kind: "fill"},
	} {
		altered := state
		altered.Events = append(altered.Events[:len(altered.Events):len(altered.Events)], event)
		altered.Version++
		if reconciliationUnchanged(altered, rec, "allocation") {
			t.Fatal("foreign economic suffix admitted", event.Kind)
		}
	}
	if reconciliationUnchanged(state, &incubationReconciliation{version: state.Version + 1}, "allocation") {
		t.Fatal("future baseline admitted")
	}
}
