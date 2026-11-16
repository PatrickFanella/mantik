package alpaca

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPaperSubmissionCheckRunsAfterDurableIntentAndFencesRefusal(t *testing.T) {
	gets, posts, checks := 0, 0, 0
	p, root := paperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/account":
			gets++
			_, _ = w.Write([]byte(`{"id":"account-1","status":"ACTIVE"}`))
		case "/v2/assets/SPY":
			gets++
			_, _ = w.Write([]byte(`{"symbol":"SPY","class":"us_equity","tradable":true}`))
		case "/v2/orders":
			posts++
			t.Error("refused submission reached POST")
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	})
	ctx := context.Background()
	expired := errors.New("fixture authority expired")
	_, e := p.SubmitChecked(ctx, testStockOrder(), func() error {
		checks++
		if gets != 2 {
			t.Fatal("check preceded account/asset reads", gets)
		}
		if _, err := os.Stat(filepath.Join(root, "paper-intent-mantik-test.json")); err != nil {
			t.Fatal("check preceded durable intent", err)
		}
		return expired
	})
	if !errors.Is(e, expired) || posts != 0 || checks != 1 {
		t.Fatal(e, posts, checks)
	}
	if _, e = p.journal.Rollover(ctx, t.TempDir()); e != nil {
		t.Fatal(e)
	}
	if e = p.journal.Close(); e != nil {
		t.Fatal(e)
	}
	j, e := OpenJournal(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := j.Close(); e != nil {
			t.Error(e)
		}
	})
	p.journal = j
	if e = p.AbortUnsubmitted(ctx, testStockOrder().ClientOrderID); e != nil {
		t.Fatal("durable refusal could not release reservation", e)
	}
	if _, e = p.Order(ctx, testStockOrder().ClientOrderID); !errors.Is(e, ErrPaperNotSubmitted) {
		t.Fatal(e)
	}
	if _, e = p.Submit(ctx, testStockOrder()); !errors.Is(e, ErrPaperNotSubmitted) || posts != 0 {
		t.Fatal("refused ID reopened", e, posts)
	}
}

func TestPaperSubmissionCheckDoesNotBlockAmbiguousRecovery(t *testing.T) {
	posts, lookups, checks := 0, 0, 0
	p, _ := paperFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/account":
			_, _ = w.Write([]byte(`{"id":"account-1","status":"ACTIVE"}`))
		case "/v2/assets/SPY":
			_, _ = w.Write([]byte(`{"symbol":"SPY","class":"us_equity","tradable":true}`))
		case "/v2/orders":
			posts++
			w.WriteHeader(503)
		case "/v2/orders:by_client_order_id":
			lookups++
			if lookups == 1 {
				w.WriteHeader(404)
			} else {
				_, _ = w.Write([]byte(testPaperResponse))
			}
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	})
	ctx := context.Background()
	check := func() error {
		checks++
		if checks > 1 {
			return errors.New("fixture quote expired")
		}
		return nil
	}
	if _, e := p.SubmitChecked(ctx, testStockOrder(), check); !errors.Is(e, ErrSubmissionUnknown) {
		t.Fatal(e)
	}
	if _, e := p.SubmitChecked(ctx, testStockOrder(), check); e != nil || posts != 1 || lookups != 2 || checks != 1 {
		t.Fatal("expired recovery retried authority or POST", e, posts, lookups, checks)
	}
}
