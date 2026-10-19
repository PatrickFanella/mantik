package paperledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRehearsalPinsAndOriginalRetryReceipts(t *testing.T) {
	ctx := context.Background()
	fixture := Fixture()
	r, err := NewRehearsal(ctx, fixture.Account)
	if err != nil {
		t.Fatal(err)
	}
	var original AppendReceipt
	var first AppendCommand
	for i, e := range fixture.Entries[:8] {
		c := AppendCommand{ReceiverID: r.Account.ReceiverID, AccountSHA256: r.Account.SHA256, ExpectedHeadSHA256: r.Result.LedgerHeadSHA256, ScopeCode: RehearsalScope, Entry: e}
		next, receipt, err := Append(ctx, r, c, e.OccurredAt.Add(time.Second))
		if err != nil || receipt.Validate(r.Account.Config.ID, c) != nil {
			t.Fatal(i, err)
		}
		if i == 0 {
			original, first = receipt, c
		}
		r = next
	}
	if r.Validate(ctx) != nil || r.Result.UnresolvedReservations != 1 || r.Result.AvailableCashCents != 55000 || len(r.Accepted) != 8 {
		t.Fatal(r)
	}
	next, retry, err := Append(ctx, r, first, time.Time{})
	if err != nil || !retry.Duplicate || !reflect.DeepEqual(original.Accepted, retry.Accepted) || !reflect.DeepEqual(next, r) {
		t.Fatal("retry changed original receipt", err)
	}
	changed := first
	changed.ExpectedHeadSHA256 = r.Result.LedgerHeadSHA256
	if _, _, err := Append(ctx, r, changed, time.Now()); !errors.Is(err, ErrConflict) {
		t.Fatal("changed head accepted", err)
	}
	future := first
	future.Entry.ID = "future"
	future.Entry.ReservationID = "future"
	future.ExpectedHeadSHA256 = r.Result.LedgerHeadSHA256
	future.Entry.OccurredAt = r.Account.CreatedAt.Add(time.Hour)
	if _, _, err := Append(ctx, r, future, r.Account.CreatedAt.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatal("future event accepted", err)
	}
	raw, _ := json.Marshal(r)
	for name, mutate := range map[string]func(*Rehearsal){"head": func(r *Rehearsal) { r.Accepted[1].Command.ExpectedHeadSHA256 = strings.Repeat("0", 64) }, "receipt": func(r *Rehearsal) { r.Accepted[0].Receipt.Sequence++ }, "clock": func(r *Rehearsal) { r.Accepted[0].AcceptedAt = r.Account.CreatedAt }, "prefix result": func(r *Rehearsal) { r.Accepted[0].ResultSHA256 = strings.Repeat("0", 64) }, "cash": func(r *Rehearsal) { r.Result.AvailableCashCents++ }, "authority": func(r *Rehearsal) { r.ExecutionAttested = true }, "duplicate": func(r *Rehearsal) { r.Accepted = append(r.Accepted, r.Accepted[0]) }, "nil": func(r *Rehearsal) { r.Accepted = nil }} {
		t.Run(name, func(t *testing.T) {
			var bad Rehearsal
			_ = json.Unmarshal(raw, &bad)
			mutate(&bad)
			bad.SHA256 = ""
			bad.SHA256 = digest(bad)
			if bad.Validate(ctx) == nil {
				t.Fatal("corrupt resealed rehearsal accepted")
			}
		})
	}
	bad := original
	bad.Accepted.Receipt.EntrySHA256 = strings.Repeat("0", 64)
	if bad.Validate(r.Account.Config.ID, first) == nil {
		t.Fatal("bad append receipt accepted")
	}
}
func TestRehearsalCapacityAndLateExactRetry(t *testing.T) {
	ctx := context.Background()
	r, _ := NewRehearsal(ctx, Fixture().Account)
	var first AppendCommand
	for i := range MaxEntries {
		e := Entry{ID: fmt.Sprintf("command-%d", i), Kind: "settle", ReservationID: "one", AmountCents: 1, OccurredAt: r.Account.CreatedAt.Add(time.Duration(i) * time.Second)}
		if i == 0 {
			e.Kind = "reserve"
			e.AmountCents = 1000
		}
		c := AppendCommand{ReceiverID: r.Account.ReceiverID, AccountSHA256: r.Account.SHA256, ExpectedHeadSHA256: r.Result.LedgerHeadSHA256, ScopeCode: RehearsalScope, Entry: e}
		if i == 0 {
			first = c
		}
		var err error
		r, _, err = appendChecked(ctx, r, c, e.OccurredAt)
		if err != nil {
			t.Fatal(i, err)
		}
	}
	if err := r.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	_, receipt, err := Append(ctx, r, first, time.Time{})
	if err != nil || !receipt.Duplicate {
		t.Fatal(err)
	}
	c := first
	c.Entry.ID = "overflow"
	c.ExpectedHeadSHA256 = r.Result.LedgerHeadSHA256
	if _, _, err := Append(ctx, r, c, time.Now()); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := r.Validate(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
