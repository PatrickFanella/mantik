package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

// BenchmarkOwnedPositiveEvaluation scales the positive fixture by sessions
// (three decisions and five feed records each) and by extra quotes per session
// (records only). "evaluate" is the portfolio evaluator over the captured
// owner inputs; "replay" is an exact assessment retry, which rederives every
// source and replays the retained catalogue.
func BenchmarkOwnedPositiveEvaluation(b *testing.B) {
	for _, sessions := range []int{2, 3, 4, 5} {
		for _, extra := range []int{0, 8, 16} {
			b.Run(fmt.Sprintf("sessions=%d/extra_quotes=%d", sessions, extra), func(b *testing.B) {
				var inputs storage.PaperEvaluationEntrySources
				m, err := storage.NewMemoryWithPortfolioEvaluator(func(ctx context.Context, in storage.PaperEvaluationEntrySources) (storage.PaperEvaluationPortfolio, error) {
					inputs = in
					return service.EvaluateOwnedPaperPortfolio(ctx, in)
				})
				if err != nil {
					b.Fatal(err)
				}
				r, _ := positiveEvaluationHTTP(b, m, nil, positiveShape{sessions: sessions, extraQuotes: extra, assessOnly: true})
				records := 0
				for _, row := range inputs.Rows {
					if row.Outcome != nil {
						records = max(records, len(row.Outcome.Basis.Observation.Feed.Records))
					}
				}
				b.Run("evaluate", func(b *testing.B) {
					for b.Loop() {
						if _, err := service.EvaluateOwnedPaperPortfolio(context.Background(), inputs); err != nil {
							b.Fatal(err)
						}
					}
					b.ReportMetric(float64(len(inputs.Rows)), "decisions")
					b.ReportMetric(float64(records), "records")
				})
				b.Run("replay", func(b *testing.B) {
					clock := &ownedDecisionClock{at: time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)}
					server := httptest.NewServer(loopbacktest.GoClientOnly(service.HandlerWithClock(m, "positive-fixture-token", clock)))
					defer server.Close()
					body, _ := json.Marshal(r.Command)
					for b.Loop() {
						request, _ := http.NewRequest("POST", server.URL+"/v1/paper-evaluation-assessments", bytes.NewReader(body))
						request.Header.Set("Authorization", "Bearer positive-fixture-token")
						response, err := http.DefaultClient.Do(request)
						if err != nil {
							b.Fatal(err)
						}
						_ = response.Body.Close()
						if response.StatusCode != 200 {
							b.Fatal("retry", response.StatusCode)
						}
					}
				})
			})
		}
	}
}
