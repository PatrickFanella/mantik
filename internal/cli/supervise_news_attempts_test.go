package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestNewsAttemptChangesRequireFreshHistory(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	ctx := context.Background()
	now := func() time.Time { return news.Fixture().ReceivedAt.Add(4 * time.Hour) }
	if _, err := backend.StartReview(ctx, catalog.ReceiverID, "changed", c.ID, c.Version, now); err != nil {
		t.Fatal(err)
	}
	reads := 0
	var out bytes.Buffer
	commands := "d " + c.ID + "\nh\na changed\na changed\nh\nr\nd " + c.ID + "\na changed\nh\na changed\nq\n"
	err := superviseNews(ctx, io.NopCloser(strings.NewReader(commands)), &out, 1,
		func(ctx context.Context) (storage.NewsCatalog, error) { return backend.NewsCases(ctx) }, nil,
		func(ctx context.Context, id string) (review.History, error) { return backend.ReviewHistory(ctx, id) },
		func(ctx context.Context, id string) (review.View, error) {
			reads++
			if reads == 1 {
				if _, err := backend.CompleteReview(ctx, catalog.ReceiverID, id, []json.RawMessage{json.RawMessage(`{"kind":"proposal","proposal":{"outcome":"abstain","reason_codes":["insufficient_evidence"],"uncertainty":"unverified"}}`)}, now); err != nil {
					t.Fatal(err)
				}
			}
			return backend.Review(ctx, id)
		})
	if err != nil || reads != 2 || strings.Count(out.String(), "EXPLICIT REVIEW ATTEMPT ARTIFACTS") != 1 || !strings.Contains(out.String(), "STALE: review attempt") {
		t.Fatal(err, reads, out.String())
	}
}

func TestNewsAttemptInvalidReadbacksAndErrorsBlockFurtherReads(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	ctx := context.Background()
	now := func() time.Time { return news.Fixture().ReceivedAt.Add(4 * time.Hour) }
	if _, err := backend.StartReview(ctx, catalog.ReceiverID, "chosen", c.ID, c.Version, now); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CompleteReview(ctx, catalog.ReceiverID, "chosen", []json.RawMessage{json.RawMessage(`{"kind":"proposal","proposal":{"outcome":"abstain","reason_codes":["insufficient_evidence"],"uncertainty":"unverified"}}`)}, now); err != nil {
		t.Fatal(err)
	}
	h, _ := backend.ReviewHistory(ctx, c.ID)
	good, _ := backend.Review(ctx, "chosen")
	if validateNewsAttempt(good, h, "chosen") != nil {
		t.Fatal("valid detail rejected")
	}
	for name, mutate := range map[string]func(*review.View){
		"id":         func(v *review.View) { v.Attempt.ID = "other" },
		"bytes":      func(v *review.View) { v.Attempt.Steps[0] += " " },
		"subject":    func(v *review.View) { v.Attempt.Pin.Subject.CaseID = catalog.Cases[1].ID },
		"profile":    func(v *review.View) { v.Attempt.Profile.SHA256 = strings.Repeat("0", 64) },
		"result":     func(v *review.View) { v.Attempt.Result.Authority = "execution" },
		"current":    func(v *review.View) { v.EvidenceCurrent = false },
		"applicable": func(v *review.View) { v.ReviewApplicable = false },
		"transport":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(good)
			var bad review.View
			_ = json.Unmarshal(raw, &bad)
			if mutate != nil {
				mutate(&bad)
			}
			if mutate != nil && validateNewsAttempt(bad, h, "chosen") == nil {
				t.Fatal("invalid detail accepted")
			}
			reads := 0
			var out bytes.Buffer
			err := superviseNews(ctx, io.NopCloser(strings.NewReader("d "+c.ID+"\nh\na chosen\na chosen\ne "+c.LastRecordKey+"\nq\n")), &out, 1,
				func(context.Context) (storage.NewsCatalog, error) { return catalog, nil },
				func(context.Context, string, bool) (jsonNewsInspection, error) {
					t.Fatal("stale source request")
					return jsonNewsInspection{}, nil
				},
				func(context.Context, string) (review.History, error) { return h, nil },
				func(context.Context, string) (review.View, error) {
					reads++
					if mutate == nil {
						return review.View{}, errors.New("offline")
					}
					return bad, nil
				})
			if err != nil || reads != 1 || strings.Contains(out.String(), "EXPLICIT REVIEW ATTEMPT ARTIFACTS") || !strings.Contains(out.String(), "STALE: review attempt") {
				t.Fatal(err, reads, out.String())
			}
		})
	}
	if validateNewsAttempt(good, h, "unrelated") == nil {
		t.Fatal("unrelated attempt accepted")
	}
	if _, err := fetchNewsAttempt(ctx, "http://invalid.invalid", "../escape"); err == nil {
		t.Fatal("unsafe attempt path")
	}
}

func TestNewsAttemptReadCancellation(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := backend.StartReview(ctx, catalog.ReceiverID, "cancelled", c.ID, c.Version, func() time.Time { return news.Fixture().ReceivedAt.Add(4 * time.Hour) }); err != nil {
		t.Fatal(err)
	}
	err := superviseNews(ctx, io.NopCloser(strings.NewReader("d "+c.ID+"\nh\na cancelled\n")), io.Discard, 1,
		func(context.Context) (storage.NewsCatalog, error) { return catalog, nil }, nil,
		func(ctx context.Context, id string) (review.History, error) { return backend.ReviewHistory(ctx, id) },
		func(context.Context, string) (review.View, error) { cancel(); return review.View{}, context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
