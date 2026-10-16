package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func TestAttemptPinsRawCompletionAndExpiredRetry(t *testing.T) {
	at := news.FixtureArticle().ReceivedAt.Add(time.Hour)
	a, err := Start("receiver-abcdefghijklmnop", "one", news.FixtureArticle(), "event", at)
	if err != nil || a.Validate() != nil {
		t.Fatal(a, err)
	}
	raw := []byte("\xffmalformed extractor bytes")
	r, err := EvaluateRecorded(context.Background(), a, raw)
	if err != nil {
		t.Fatal(err)
	}
	done, err := Commit(a, raw, r, func() time.Time { return a.Deadline })
	if err != nil || done.Status != "expired" || done.Result.Outcome != "abstain" || done.Validate() != nil {
		t.Fatal(done, err)
	}
	retry, err := Commit(done, raw, Result{}, func() time.Time { t.Fatal("completed retry sampled clock"); return time.Time{} })
	if err != nil || !reflect.DeepEqual(retry, done) {
		t.Fatal("retry changed receipt", err)
	}
	if _, err = Commit(done, append(raw, ' '), Result{}, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("changed bytes recovered old receipt", err)
	}
	if _, err = Commit(a, raw, r, func() time.Time { return at.Add(-time.Second) }); !errors.Is(err, ErrConflict) {
		t.Fatal("backward completion clock accepted", err)
	}
}

func TestRetainedAttemptRecomputesChecksDespiteResealedHashes(t *testing.T) {
	a, _ := Start("receiver-abcdefghijklmnop", "one", news.FixtureArticle(), "event", news.FixtureArticle().ReceivedAt.Add(time.Hour))
	raw, _ := json.Marshal(FixtureResponse())
	r, _ := EvaluateRecorded(context.Background(), a, raw)
	done, err := Commit(a, raw, r, func() time.Time { return a.StartedAt.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Attempt){
		"contract":       func(a *Attempt) { a.Contract.TimeoutSeconds++ },
		"article":        func(a *Attempt) { a.Article.Original += "changed" },
		"source pin":     func(a *Attempt) { a.ArticleSHA256 = strings.Repeat("a", 64) },
		"deadline":       func(a *Attempt) { a.Deadline = a.Deadline.Add(time.Second) },
		"claim":          func(a *Attempt) { a.Result.Claims[0].Evidence[0].Quote = "false source" },
		"checks":         func(a *Attempt) { a.Result.Checks[0].Status = "not_run" },
		"authority":      func(a *Attempt) { a.Result.QualificationCredit = true },
		"artifact":       func(a *Attempt) { a.Result.Artifact.RawBase64 = "YQ==" },
		"raw size":       func(a *Attempt) { a.Result.Artifact.Bytes++ },
		"expiry":         func(a *Attempt) { a.Status = "expired" },
		"pending result": func(a *Attempt) { a.Status = "pending"; a.CompletedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := copyJSON(done)
			change(&copy)
			if copy.Result != nil {
				copy.Result.SHA256 = ""
				copy.Result.SHA256 = hash(*copy.Result)
			}
			copy = seal(copy)
			if copy.Validate() == nil {
				t.Fatal("resealed corruption accepted")
			}
		})
	}
	h, err := MakeHistory(a.ReceiverID, []Attempt{done, a})
	if err == nil {
		t.Fatal("duplicate attempt IDs accepted", h)
	}
	h, err = MakeHistory(a.ReceiverID, []Attempt{done})
	if err != nil || h.Validate() != nil {
		t.Fatal(err)
	}
	h.StatusCounts["completed"]++
	h.SHA256 = ""
	h.SHA256 = hash(h)
	if h.Validate() == nil {
		t.Fatal("resealed count corruption accepted")
	}
}
