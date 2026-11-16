package service

import (
	"context"
	"fmt"
	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/risk"
	"testing"
	"time"
)

func TestDailyBriefQuarantineRecovery(t *testing.T) {
	ctx := context.Background()
	r, req, base := reviewFixture(t, false)
	previous := base.At
	var last account.StrategyReview
	for day := 1; day <= 24; day++ {
		price := "91.5"
		if day > 1 {
			price = fmt.Sprintf("%.2f", 100+float64(day)*0.2)
		}
		in := reviewObservation(base.At.Add(time.Duration(day)*24*time.Hour), price, previous)
		var err error
		last, err = (StrategyReviewService{r.Store}).Review(ctx, r.BookID, fmt.Sprint("probe-day-", day), in)
		if err != nil {
			t.Fatal(err)
		}
		previous = in.At
	}
	next := nextForwardCycle(req, "probe-recovered-cycle", previous.Add(time.Second))
	r.now = func() time.Time { return next.ExecuteAt }
	r.Safety = startIncubationTestSafety(t, r, next.ExecuteAt.Add(-time.Second))
	sized, err := r.Run(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after recovered review next distribution budget=%s", sized.Distribution.Sleeves[0].Budget)
	previous = next.ExecuteAt
	b, err := r.Store.CappedBook(ctx, r.BookID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := BuildDailyBrief(BriefSource{Book: b, AsOf: previous}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("last finalized action=%s scale=%s; brief state=%s", last.Action, last.Scale, out.Strategies[0].State)
	if last.Action != "grow" || last.Scale != "1.00" {
		t.Fatal("fixture did not recover")
	}
	if out.Strategies[0].State == "review_quarantine" {
		t.Error("fully restored sleeve still shown in quarantine")
	}
	for i, action := range []string{"pause_strategy", "retire_strategy", "resume_strategy"} {
		at := previous.Add(time.Duration(i+1) * time.Second)
		b, err = r.Store.ApplyCappedBook(ctx, r.BookID, b.Version, account.BookCommand{ID: fmt.Sprint("recovered-control-", i), Kind: "risk_control", At: at, RiskControl: &risk.Control{Action: action, StrategyID: last.StrategyID}})
		if err != nil {
			t.Fatal(err)
		}
		out, err = BuildDailyBrief(BriefSource{Book: b, AsOf: at}, nil)
		want := "retired"
		if i == 0 {
			want = "owner_paused"
		}
		if err != nil || out.Strategies[0].State != want {
			t.Fatal("recovery overrode owner control", action, out.Strategies, err)
		}
	}
}
