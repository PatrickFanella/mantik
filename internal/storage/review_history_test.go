package storage

import (
	"context"
	"sync"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestMemoryReviewHistoryClosureSnapshot(t *testing.T) { reviewHistoryClosure(t, NewMemory()) }
func TestPostgresContractsReviewHistoryClosureSnapshot(t *testing.T) {
	reviewHistoryClosure(t, timerPostgres(t, "historyclose"))
}

func reviewHistoryClosure(t *testing.T, s reviewBackend) {
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	now := func() time.Time { return r.ReceivedAt.Add(time.Hour) }
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	id := receipt.Matches[0].CaseID
	started, err := s.StartReview(ctx, catalog.ReceiverID, "race", id, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	steps := reviewSteps(started.Attempt.Pin.Group.Claims[0].Claim.Key)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		if _, err := s.CloseNewsCase(ctx, catalog.ReceiverID, id, 1, now); err != nil {
			t.Error(err)
		}
	})
	wg.Go(func() {
		<-start
		if _, err := s.CompleteReview(ctx, catalog.ReceiverID, "race", steps, now); err != nil {
			t.Error(err)
		}
	})
	for range 8 {
		wg.Go(func() {
			<-start
			h, err := s.ReviewHistory(ctx, id)
			if err != nil || h.Validate() != nil || h.AttemptCount != 1 {
				t.Error("mixed closure/completion snapshot", h, err)
			}
		})
	}
	close(start)
	wg.Wait()
	h, err := s.ReviewHistory(ctx, id)
	if err != nil || h.Validate() != nil || h.Case.Status != "closed" || h.Attempts[0].ReviewApplicable || h.Attempts[0].EvidenceCurrent {
		t.Fatal(h, err)
	}
}
