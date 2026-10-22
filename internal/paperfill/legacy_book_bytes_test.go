package paperfill

import (
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"testing"
)

func TestLegacyBookBytes(t *testing.T) {
	b := mustBook(t, model(), bookOrders())
	var p BookProjection
	for i := 2; i < 5; i++ {
		q := quote("capture-"+string(rune('a'+i)), i)
		if i > 2 {
			q.OccurredAt = start.Add(2000000000)
			q.AskCents += int64(i)
			q.BidCents -= int64(i)
		}
		b, p, _ = applyBook(t, b, BookCommand{ID: q.ID, At: q.ReceivedAt, Quote: &q})
	}
	got := workerplan.Hash(struct {
		Book       Book
		Projection BookProjection
	}{b, p})
	if got != "ed6fbcd6e3af389d4dd76655b4cb42f82e2482d9a7cdb8ee663ebdbb1c863d45" {
		t.Fatal("legacy serialized replay changed", got)
	}
}
