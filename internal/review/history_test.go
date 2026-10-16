package review_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestReviewHistoryRejectsResealedMetadataCorruption(t *testing.T) {
	ctx := context.Background()
	s := storage.NewMemory()
	r := news.Fixture()
	now := func() time.Time { return r.ReceivedAt.Add(time.Hour) }
	catalog, _ := s.NewsCases(ctx)
	receipt, err := s.AdmitNews(ctx, catalog.ReceiverID, r, now)
	if err != nil {
		t.Fatal(err)
	}
	caseID := receipt.Matches[0].CaseID
	for _, id := range []string{"a-pending", "z-abstention"} {
		if _, err = s.StartReview(ctx, catalog.ReceiverID, id, caseID, 1, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.CompleteReview(ctx, catalog.ReceiverID, "z-abstention", []json.RawMessage{json.RawMessage(`{"kind":"proposal","narrative":"RAW MODEL MUST STAY TARGETED"}`)}, now); err != nil {
		t.Fatal(err)
	}
	h, err := s.ReviewHistory(ctx, caseID)
	if err != nil || h.Validate() != nil || h.AttemptCount != 2 || h.StatusCounts["pending"] != 1 || h.OutcomeCounts["abstain"] != 1 {
		t.Fatal(h, err)
	}
	raw, _ := json.Marshal(h)
	if strings.Contains(string(raw), "RAW MODEL MUST STAY TARGETED") {
		t.Fatal("raw model copied into history")
	}
	for name, mutate := range map[string]func(*review.History){
		"count":          func(h *review.History) { h.AttemptCount++ },
		"status-count":   func(h *review.History) { h.StatusCounts["pending"]++ },
		"outcome-count":  func(h *review.History) { h.OutcomeCounts["abstain"]++ },
		"artifact-count": func(h *review.History) { h.ArtifactCount++ },
		"authority":      func(h *review.History) { h.QualificationCredit = true },
		"receiver":       func(h *review.History) { h.ReceiverID = "receiver-other_receiver_1" },
		"case":           func(h *review.History) { h.Case.Version = 0 },
		"current-flag":   func(h *review.History) { h.Attempts[0].EvidenceCurrent = false },
		"profile":        func(h *review.History) { h.Attempts[0].ProfileSHA256 = strings.Repeat("f", 64) },
		"subject":        func(h *review.History) { h.Attempts[0].Subject.EventKey = "other" },
		"pending-result": func(h *review.History) { h.Attempts[0].Outcome = "abstain" },
		"clock":          func(h *review.History) { h.Attempts[1].CompletedAt = h.Attempts[1].StartedAt.Add(-time.Second) },
		"checks":         func(h *review.History) { h.Attempts[1].Checks[0].Passed = !h.Attempts[1].Checks[0].Passed },
		"order":          func(h *review.History) { h.Attempts[0], h.Attempts[1] = h.Attempts[1], h.Attempts[0] },
		"duplicate":      func(h *review.History) { h.Attempts = append(h.Attempts, h.Attempts[0]) },
		"nil":            func(h *review.History) { h.Attempts = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var bad review.History
			_ = json.Unmarshal(raw, &bad)
			mutate(&bad)
			bad.SHA256 = ""
			body, _ := json.Marshal(bad)
			bad.SHA256 = news.SHA256(string(body))
			if bad.Validate() == nil {
				t.Fatal("invalid resealed report accepted")
			}
		})
	}
	bad := h
	bad.SHA256 = "changed"
	if bad.Validate() == nil {
		t.Fatal("changed hash accepted")
	}
}
