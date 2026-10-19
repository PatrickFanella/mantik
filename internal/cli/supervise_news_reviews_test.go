package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestNewsSupervisionReviewHistoryHTTP(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	ctx := context.Background()
	now := func() time.Time { return news.Fixture().ReceivedAt.Add(4 * time.Hour) }
	for _, id := range []string{"pending-review", "completed-review"} {
		if _, err := backend.StartReview(ctx, catalog.ReceiverID, id, c.ID, c.Version, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := backend.CompleteReview(ctx, catalog.ReceiverID, "completed-review", []json.RawMessage{json.RawMessage(`{"kind":"proposal","proposal":{"outcome":"abstain","reason_codes":["insufficient_evidence"],"uncertainty":"unverified"}}`)}, now); err != nil {
		t.Fatal(err)
	}
	before, _ := backend.ReviewHistory(ctx, c.ID)
	handler := service.Handler(backend, "history-token")
	t.Setenv("MANTIK_API_TOKEN", "history-token")
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("mutation: %s", r.Method)
		}
		paths = append(paths, r.URL.RequestURI())
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	var out bytes.Buffer
	commands := "a completed-review\nh\nd " + catalog.Cases[1].ID + "\nh\nd " + c.ID + "\na completed-review\nh\na pending-review\na completed-review\na ../escape\na unknown-attempt\nh extra\nn\nh\np\nh\nr\nh\nq\n"
	err := superviseNews(ctx, io.NopCloser(strings.NewReader(commands)), &out, 1,
		func(ctx context.Context) (storage.NewsCatalog, error) { return fetchNewsCatalog(ctx, server.URL) }, nil,
		func(ctx context.Context, id string) (review.History, error) {
			return fetchNewsHistory(ctx, server.URL, id)
		}, func(ctx context.Context, id string) (review.View, error) {
			return fetchNewsAttempt(ctx, server.URL, id)
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/v1/news-cases", "/v1/news-cases/" + c.ID + "/reviews", "/v1/news-reviews/pending-review", "/v1/news-reviews/completed-review", "/v1/news-cases"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatal(paths)
	}
	for _, text := range []string{"CURRENT REVIEW HISTORY", "EXPLICIT REVIEW ATTEMPT ARTIFACTS", `"pending-review"`, `"completed-review"`, `"review_applicable": true`, `"review_applicable": false`, `"outcome": "abstain"`, `"qualification_credit": false`} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing", text)
		}
	}
	if strings.Count(out.String(), "EXPLICIT REVIEW ATTEMPT ARTIFACTS") != 2 {
		t.Fatal("unexpected artifact reads", out.String())
	}
	after, _ := backend.ReviewHistory(ctx, c.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("history viewer mutated attempts")
	}
}

func TestNewsSupervisionReviewCaseChangedRequiresRefresh(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	ctx := context.Background()
	now := func() time.Time { return news.Fixture().ReceivedAt.Add(4 * time.Hour) }
	if _, err := backend.StartReview(ctx, catalog.ReceiverID, "old-review", c.ID, c.Version, now); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.CompleteReview(ctx, catalog.ReceiverID, "old-review", []json.RawMessage{json.RawMessage(`{"kind":"proposal","proposal":{"outcome":"abstain","reason_codes":["insufficient_evidence"],"uncertainty":"unverified"}}`)}, now); err != nil {
		t.Fatal(err)
	}
	loads, histories := 0, 0
	var out bytes.Buffer
	err := superviseNews(ctx, io.NopCloser(strings.NewReader("d "+c.ID+"\nh\nh\ne "+c.LastRecordKey+"\nr\nd "+c.ID+"\nh\nq\n")), &out, 1,
		func(ctx context.Context) (storage.NewsCatalog, error) { loads++; return backend.NewsCases(ctx) },
		func(context.Context, string, bool) (jsonNewsInspection, error) {
			t.Fatal("source read while stale")
			return jsonNewsInspection{}, nil
		},
		func(ctx context.Context, id string) (review.History, error) {
			histories++
			if histories == 1 {
				if _, err := backend.CloseNewsCase(ctx, catalog.ReceiverID, c.ID, c.Version, now); err != nil {
					t.Fatal(err)
				}
			}
			return backend.ReviewHistory(ctx, id)
		}, nil)
	if err != nil || loads != 2 || histories != 2 {
		t.Fatal(err, loads, histories)
	}
	if strings.Count(out.String(), "CURRENT REVIEW HISTORY") != 1 || !strings.Contains(out.String(), "STALE: review history") || !strings.Contains(out.String(), `"review_applicable": false`) || strings.Contains(out.String(), `"review_applicable": true`) {
		t.Fatal(out.String())
	}
}

func TestNewsSupervisionRefusesInvalidHistory(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	good, err := backend.ReviewHistory(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*review.History){
		"hash":      func(h *review.History) { h.SHA256 = "bad" },
		"authority": func(h *review.History) { h.QualificationCredit = true },
		"count":     func(h *review.History) { h.AttemptCount++ },
		"receiver":  func(h *review.History) { h.ReceiverID = "receiver-other_receiver_1" },
		"case":      func(h *review.History) { h.Case = catalog.Cases[1] },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(good)
			var bad review.History
			_ = json.Unmarshal(raw, &bad)
			mutate(&bad)
			reads := 0
			var out bytes.Buffer
			err := superviseNews(context.Background(), io.NopCloser(strings.NewReader("d "+c.ID+"\nh\nh\nn\nq\n")), &out, 1,
				func(context.Context) (storage.NewsCatalog, error) { return catalog, nil }, nil,
				func(context.Context, string) (review.History, error) { reads++; return bad, nil }, nil)
			if err != nil || reads != 1 || strings.Contains(out.String(), "CURRENT REVIEW HISTORY") || !strings.Contains(out.String(), "STALE: review history") {
				t.Fatal(err, reads, out.String())
			}
		})
	}
	// A valid, independently sealed readback from another receiver must also fail.
	other, err := review.MakeHistory("receiver-other_receiver_1", c, nil)
	if err != nil || other.Validate() != nil {
		t.Fatal(err)
	}
	if validateNewsHistory(other, catalog.ReceiverID, c) == nil {
		t.Fatal("cross receiver history accepted")
	}
	closed := c
	closed.Status = "closed"
	other, err = review.MakeHistory(catalog.ReceiverID, closed, nil)
	if err != nil || other.Validate() != nil || validateNewsHistory(other, catalog.ReceiverID, c) == nil {
		t.Fatal("changed case accepted", err)
	}
	if _, err = fetchNewsHistory(context.Background(), "http://invalid.invalid", "../escape"); err == nil {
		t.Fatal("unsafe ID accepted")
	}
}
