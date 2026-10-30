package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
	"git.subcult.tv/subculture-collective/mantik/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool               *pgxpool.Pool
	portfolioEvaluator PaperPortfolioEvaluator
}

func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid DATABASE_URL")
	}
	if config.ConnConfig.Database != "mantik" && !strings.HasPrefix(config.ConnConfig.Database, "mantik_test_") {
		return nil, errors.New("bootstrap requires database mantik or mantik_test_*; existing Augr databases are not supported")
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("cannot initialize PostgreSQL pool")
	}
	p := &Postgres{pool: pool}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("cannot connect to PostgreSQL")
	}
	if err := p.migrate(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("bootstrap schema: %w", err)
	}
	return p, nil
}

func (p *Postgres) migrate(ctx context.Context) error {
	statements := []string{migrations.Bootstrap, migrations.TriggerRouting, migrations.IndependentTimers, migrations.DurableActions, migrations.OperationalReadiness, migrations.AttributedRouting, migrations.ResearchReceiver, migrations.FeedCollectors, migrations.CollectorAdmissions, migrations.FeedEvidence, migrations.NewsCases, migrations.NewsReviews, migrations.HypothesisPublications, migrations.HypothesisTrials, migrations.TrialAttemptIntents, migrations.NativeTrialRuns, migrations.ExternalTrialJobs, migrations.ExternalTrialAttempts, migrations.ReviewProfiles, migrations.ReviewHypothesisInventories, migrations.NewsExtractions, migrations.ReviewedExtractionAdmissions, migrations.LocalExtractionCaptures, migrations.LocalCaptureAdmissions, migrations.SwitchyardExtractionCaptures, migrations.SwitchyardCaptureAdmissions, migrations.PaperAccounts, migrations.PaperLedgerRehearsals, migrations.FixtureCapital, migrations.CandidateRegistrations, migrations.CandidateWorkerIntents, migrations.CandidateWorkerOutcomes, migrations.CandidateGenerationIntents, migrations.CandidateGenerationOutcomes, migrations.GeneratedCandidateLinks, migrations.AllocationPolicies, migrations.PaperStrategies, migrations.PaperDecisions, migrations.OwnedAllocations, migrations.PaperOutcomes, migrations.SocialWatches, migrations.OwnedAdmissionChecks, migrations.OwnedExecutionCosts, migrations.OwnedProtectiveFunding, migrations.DecisionReviews, migrations.PaperEvaluationDeclarations, migrations.OwnedAdmissionBindings, migrations.OwnedEvaluationContext, migrations.CompleteEvaluationCaptures, migrations.PaperEvaluationAssessments, migrations.AllocationAssessmentContext, migrations.AssessmentDecisionReadiness, migrations.OwnedAdmissionReviews, migrations.NativeWarmUpAssessments, migrations.NativeCalibrationApplicability, migrations.SourceObservationClock, migrations.DecisionSourceMembership, migrations.OutcomeSourceMembership, migrations.DecisionReviewSources, migrations.DistinctEvaluationVariants, migrations.KnownSourceLeakageAssessments, migrations.AssessmentFailureBindings, migrations.ExecutionSafetyBindings, migrations.AdmissionSafetyBindings, migrations.OwnedProposalModelExpiry, migrations.OwnedExcludedAssessmentFailure, migrations.SocialResearchRequests, migrations.SocialResearchSourceApplicability, migrations.RetainedSocialResearchSelection, migrations.RetainedCopyResearchSelection, migrations.CorporateActionSourceCustody, migrations.EvaluationCorporateSourceCuts, migrations.JournalCorporateSources, migrations.PredeclaredCorporateSelection, migrations.ObservedPortfolioAssessments, migrations.PredeclaredHoldoutAssessments, migrations.ObservedBenchmarkAssessments, migrations.SocialResearchDispatch, migrations.SocialResearchWorkerHealth, migrations.NativeFeatureCausality, migrations.DisjointSampleDesign, migrations.SearchHistoryMultiplicity, migrations.CorporateReferenceSources}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(71423001)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS mantik_schema (version integer PRIMARY KEY, sha256 text NOT NULL)"); err != nil {
		return err
	}
	var count, maximum, minimum int
	if err = tx.QueryRow(ctx, "SELECT count(*),coalesce(max(version),0),coalesce(min(version),0) FROM mantik_schema").Scan(&count, &maximum, &minimum); err != nil {
		return err
	}
	if maximum > len(statements) || count != maximum || count > 0 && minimum != 1 {
		return errors.New("unsupported or incomplete schema version")
	}
	for index, sql := range statements {
		version := index + 1
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(sql)))
		var existing string
		err = tx.QueryRow(ctx, "SELECT sha256 FROM mantik_schema WHERE version=$1", version).Scan(&existing)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err = tx.Exec(ctx, sql); err != nil {
				return err
			}
			if version == 1 {
				raw, err := json.Marshal(workflow.InitialState())
				if err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, "INSERT INTO mantik_state(id,payload) VALUES('demo',$1)", raw); err != nil {
					return err
				}
			}
			if version == 2 || version == 3 {
				if err := backfillRouting(ctx, tx); err != nil {
					return err
				}
			}
			if version == 3 {
				if err := backfillTimers(ctx, tx); err != nil {
					return err
				}
			}
			if version == 5 {
				if err := backfillOperations(ctx, tx); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, "INSERT INTO mantik_schema VALUES($1,$2)", version, digest); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if existing != digest {
			return errors.New("migration checksum mismatch")
		}
	}

	return tx.Commit(ctx)
}

func (p *Postgres) Ingest(ctx context.Context, o domain.Observation) (domain.Receipt, error) {
	o, raw, err := canonical(o)
	if err != nil {
		return domain.Receipt{}, err
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.Receipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	// One aggregate in the bootstrap. The row lock fences concurrent service
	// instances and keeps observation deduplication in the same transaction.
	var stateRaw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo' FOR UPDATE").Scan(&stateRaw); err != nil {
		return domain.Receipt{}, err
	}
	var previous, receiptRaw []byte
	err = tx.QueryRow(ctx, "SELECT payload,receipt FROM mantik_observations WHERE id=$1", o.ID).Scan(&previous, &receiptRaw)
	if err == nil {
		var existing domain.Observation
		if err = json.Unmarshal(previous, &existing); err != nil {
			return domain.Receipt{}, err
		}
		if existing != o {
			return domain.Receipt{}, ErrConflict
		}
		var receipt domain.Receipt
		if err = json.Unmarshal(receiptRaw, &receipt); err != nil {
			return receipt, err
		}
		receipt.Duplicate = true
		return receipt, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Receipt{}, err
	}
	var state domain.State
	if err = json.Unmarshal(stateRaw, &state); err != nil {
		return domain.Receipt{}, err
	}
	state, events, err := workflow.Queue(state, o)
	if err != nil {
		return domain.Receipt{}, err
	}
	receipt := domain.Receipt{ObservationID: o.ID, Events: events}
	receiptRaw, err = json.Marshal(receipt)
	if err != nil {
		return domain.Receipt{}, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO mantik_observations VALUES($1,$2,$3)", o.ID, raw, receiptRaw); err != nil {
		return domain.Receipt{}, err
	}
	for _, event := range events {
		eventRaw, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return domain.Receipt{}, marshalErr
		}
		if _, err = tx.Exec(ctx, "INSERT INTO mantik_events(id,observation_id,payload) VALUES($1,$2,$3)", event.ID, o.ID, eventRaw); err != nil {
			return domain.Receipt{}, err
		}
	}
	stateRaw, err = json.Marshal(state)
	if err != nil {
		return domain.Receipt{}, err
	}
	if _, err = tx.Exec(ctx, "UPDATE mantik_state SET payload=$1 WHERE id='demo'", stateRaw); err != nil {
		return domain.Receipt{}, err
	}
	return receipt, tx.Commit(ctx)
}

func (p *Postgres) Snapshot(ctx context.Context) (domain.Snapshot, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.Snapshot{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	snapshot := domain.Snapshot{Mode: "paper_fixture", Events: []domain.Event{}}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT payload FROM mantik_state WHERE id='demo'").Scan(&raw); err != nil {
		return snapshot, err
	}
	if err = json.Unmarshal(raw, &snapshot.State); err != nil {
		return snapshot, err
	}
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM mantik_observations").Scan(&snapshot.ObservationCount); err != nil {
		return snapshot, err
	}
	rows, err := tx.Query(ctx, "SELECT payload FROM mantik_events ORDER BY sequence")
	if err != nil {
		return snapshot, err
	}
	defer rows.Close()
	for rows.Next() {
		var event domain.Event
		if err = rows.Scan(&raw); err != nil {
			return snapshot, err
		}
		if err = json.Unmarshal(raw, &event); err != nil {
			return snapshot, err
		}
		snapshot.Events = append(snapshot.Events, event)
	}
	if err = rows.Err(); err != nil {
		return snapshot, err
	}
	return snapshot, tx.Commit(ctx)
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }
func (p *Postgres) Close()                         { p.pool.Close() }
