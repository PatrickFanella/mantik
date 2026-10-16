package storage_test

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
	"github.com/jackc/pgx/v5"
)

func externalPostgres(t *testing.T, name string) (*storage.Postgres, string) {
	t.Helper()
	base := os.Getenv("MANTIK_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("use make test-integration")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	database := u.Path[1:] + "_" + name
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{database}.Sanitize()); err != nil {
			t.Error(err)
		}
		_ = conn.Close(ctx)
	})
	u.Path = "/" + database
	config := u.String()
	s, err := storage.OpenPostgres(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, config
}
func externalRegistration(t *testing.T, s externalBackend) (trial.Registration, time.Time) {
	t.Helper()
	ctx := context.Background()
	source := news.Fixture()
	at := source.ReceivedAt.Add(time.Hour)
	now := func() time.Time { return at }
	catalog, err := s.NewsCases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiver := catalog.ReceiverID
	admitted, err := s.AdmitNews(ctx, receiver, source, now)
	if err != nil {
		t.Fatal(err)
	}
	started, err := s.StartReview(ctx, receiver, "review", admitted.Matches[0].CaseID, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	dataset, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := experiment.DefaultRecipe(dataset)
	if err != nil {
		t.Fatal(err)
	}
	d := hypothesis.Fixture()
	d.HorizonSeconds = int64(recipe.WindowEnd.Sub(recipe.WindowStart) / time.Second)
	d.Methods = nil
	for _, m := range recipe.Methods {
		pin, _, err := hypothesis.PinMethod(m.ID)
		if err != nil {
			t.Fatal(err)
		}
		d.Methods = append(d.Methods, pin)
	}
	claim := started.Attempt.Pin.Group.Claims[0]
	d.Origins = []hypothesis.Origin{{Reference: claim.Reference, ClaimKey: claim.Claim.Key, RecordingSHA256: claim.Claim.RecordingSHA256}}
	tool, _ := json.Marshal(investigation.Step{Kind: "tool", Tool: &investigation.ToolCall{Name: "inspect_news_claim", ClaimKey: claim.Claim.Key}})
	proposal, _ := json.Marshal(investigation.Step{Kind: "proposal", Proposal: &investigation.Proposal{Outcome: "propose_hypothesis", Uncertainty: "unverified", ReasonCodes: []string{"synthetic_question"}, ClaimKeys: []string{claim.Claim.Key}, Candidate: &investigation.Candidate{Kind: "research_hypothesis", ClaimCode: d.Mechanism.Code, RefutationCode: d.Refutation.Code, EvidenceKeys: []string{claim.Claim.Key}}}})
	if _, err = s.CompleteReview(ctx, receiver, "review", []json.RawMessage{tool, proposal}, now); err != nil {
		t.Fatal(err)
	}
	p, err := s.PublishHypothesis(ctx, receiver, "publication", "review", d, now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.RegisterTrial(ctx, trial.Command{ReceiverID: receiver, RequestID: "trial", PublicationID: p.ID, Recipe: recipe}, now)
	if err != nil {
		t.Fatal(err)
	}
	return r, at
}
