package account

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRiskCheckpointFullLegacyTailAcceptsProtectiveFill(t *testing.T) {
	at := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	s, err := NewCappedState(DefaultCappedConfig("legacy-fill", "paper", Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	entry := BookOrder{ID: "entry", Symbol: "SPY", Quantity: "0.1", Limit: "100", Fee: "0", Side: "buy", At: at}
	exit := entry
	exit.ID, exit.Side, exit.ReduceOnly = "protective", "sell", true
	fill := BookFill{ID: "entry-fill", OrderID: entry.ID, VenueOrderID: "entry-venue", Symbol: "SPY", Quantity: "0.1", Price: "100", Fee: "0", Side: "buy", At: at, SourceSHA256: strings.Repeat("a", 64)}
	marks := map[string]string{"SPY": "100"}
	for _, c := range []BookCommand{
		{ID: "entry-reserve", Kind: "reserve", At: at, Order: &entry, Marks: marks},
		{ID: "entry-bind", Kind: "bind", At: at, OrderID: entry.ID, VenueOrderID: fill.VenueOrderID},
		{ID: fill.ID, Kind: "fill", At: at, Fill: &fill, Marks: marks},
		{ID: "exit-reserve", Kind: "reserve", At: at, Order: &exit, Marks: marks},
		{ID: "exit-bind", Kind: "bind", At: at, OrderID: exit.ID, VenueOrderID: "exit-venue"},
	} {
		s, err = ApplyCappedCommand(s, c)
		if err != nil {
			t.Fatal(c.Kind, err)
		}
	}
	for len(s.Events) < 4096 {
		i := len(s.Events)
		s.Events = append(s.Events, BookCommand{ID: fmt.Sprintf("legacy-observation-%d", i), Kind: "risk_observe", At: at.Add(time.Duration(i) * time.Second), Marks: marks})
	}
	s.Version = 4096
	fill.ID, fill.OrderID, fill.VenueOrderID, fill.Side = "protective-fill", exit.ID, "exit-venue", "sell"
	fill.At = at.Add(time.Second)
	now := at.Add(4097 * time.Second)
	next, err := ApplyCappedCommand(s, BookCommand{ID: fill.ID, Kind: "fill", At: now, Fill: &fill, Marks: marks})
	if err != nil {
		t.Fatal("full legacy journal stranded an executed protective fill", err)
	}
	snap, err := SnapshotCapped(next, marks, now)
	if err != nil || snap.Positions["SPY"] != "0.000000000" || next.Version != 4097 || len(next.Events) > 8 {
		t.Fatal("protective fill or logical history lost", next.Version, len(next.Events), snap, err)
	}
}

func TestRiskCheckpointRecoversFullLegacyTail(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	legacy, err := NewCappedState(DefaultCappedConfig("legacy", "paper", Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4096 {
		legacy.Events = append(legacy.Events, BookCommand{ID: fmt.Sprint(i), Kind: "risk_observe", At: at.Add(time.Duration(i) * time.Second)})
		legacy.Version++
	}
	snap, err := SnapshotCapped(legacy, nil, at.Add(4096*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	oversized := legacy
	oversized.Events = append(oversized.Events, BookCommand{ID: "4096", Kind: "risk_observe", At: at.Add(4096 * time.Second)})
	oversized.Version++
	if _, err = SnapshotCapped(oversized, nil, at.Add(4096*time.Second)); err == nil {
		t.Fatal("raw journal limit removed")
	}
	before, _ := json.Marshal(legacy)
	next, err := ApplyCappedCommand(legacy, BookCommand{ID: "next", Kind: "risk_observe", At: at.Add(4096 * time.Second)})
	if err != nil || next.Version != 4097 || len(next.Events) != 2 {
		t.Fatal(next.Version, len(next.Events), err)
	}
	after, _ := json.Marshal(legacy)
	if string(before) != string(after) {
		t.Fatal("legacy bytes changed")
	}
	if next.Events[0].RiskCheckpoint.Count != 4096 || !reflect.DeepEqual(next.Events[0].RiskCheckpoint.State, snap.Risk) {
		t.Fatal("legacy Observe result changed")
	}
}

func TestRiskCheckpointLeavesSealedPrefixesAndVersionCounts(t *testing.T) {
	at := time.Date(2026, 11, 2, 15, 0, 0, 0, time.UTC)
	state, err := NewCappedState(DefaultCappedConfig("prefix", "paper", Cash, at))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		state, err = ApplyCappedCommand(state, BookCommand{ID: fmt.Sprint(i), Kind: "risk_observe", At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err = ApplyCappedCommand(state, BookCommand{ID: "halt", Kind: "halt", At: at})
	if err != nil {
		t.Fatal(err)
	}
	sealed := CappedPrefix(state, len(state.Events))
	raw, _ := json.Marshal(sealed)
	for i := range 8 {
		state, err = ApplyCappedCommand(state, BookCommand{ID: fmt.Sprint(i + 10), Kind: "risk_observe", At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	prefix := CappedPrefix(state, len(sealed.Events))
	after, _ := json.Marshal(prefix)
	if string(raw) != string(after) || prefix.Version != 9 || state.Version != 17 {
		t.Fatal("sealed prefix/hash or logical version changed")
	}
	byVersion, err := CappedVersionPrefix(state, sealed.Version)
	if err != nil || !reflect.DeepEqual(byVersion, sealed) {
		t.Fatal("logical prefix did not recover sealed state", err)
	}
	if _, err = CappedVersionPrefix(state, 1); err == nil {
		t.Fatal("cut inside a folded run pretended to be an exact prefix")
	}
	snap, err := SnapshotCapped(state, nil, at)
	if err != nil || !snap.Halted {
		t.Fatal("halt lost", snap, err)
	}
}
