package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestRetainedReviewCLI(t *testing.T) {
	s := storage.NewMemory()
	ctx := context.Background()
	catalog, _ := s.NewsCases(ctx)
	r := news.Fixture()
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, r, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.Handler(s, "review-cli-token"))
	defer server.Close()
	t.Setenv("MANTIK_API_TOKEN", "review-cli-token")
	var out bytes.Buffer
	run := func(args ...string) review.View {
		t.Helper()
		out.Reset()
		args = append(args, "--api-url", server.URL)
		if err := Run(ctx, args, &out, &out); err != nil {
			t.Fatal(err)
		}
		var view review.View
		if err = json.Unmarshal(out.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view
	}
	started := run("news-review-start", "--id", "review-one", "--receiver-id", catalog.ReceiverID, "--case", receipt.Matches[0].CaseID, "--version", "1")
	if started.Attempt.Status != "pending" {
		t.Fatal(started)
	}
	fixture := filepath.Join(t.TempDir(), "model.json")
	if err = os.WriteFile(fixture, []byte(`[{"kind":"proposal","proposal":{"outcome":"abstain","reason_codes":["insufficient_evidence"],"uncertainty":"unverified"}}]`), 0600); err != nil {
		t.Fatal(err)
	}
	complete := run("news-review-complete", "--id", "review-one", "--receiver-id", catalog.ReceiverID, "--model-fixture", fixture)
	if !complete.ReviewApplicable || complete.Attempt.Result.Outcome != "abstain" {
		t.Fatal(complete)
	}
	actual := run("news-review", "--id", "review-one")
	if actual.Attempt.Result.ID != complete.Attempt.Result.ID {
		t.Fatal("readback changed result")
	}
	for _, args := range [][]string{{"news-review"}, {"news-review-start", "--id", "empty"}, {"news-review-complete", "--id", "review-one", "--model-fixture", fixture}} {
		out.Reset()
		if err = Run(ctx, args, &out, &out); err == nil {
			t.Fatal("incomplete arguments accepted")
		}
	}
}
