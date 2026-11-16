package forward

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

const AssessmentPolicy = "prospective-purged-day-sign-v1"

// PendingSearchTotal marks a storage-observed unavailable family count on a
// protective collection. It is not a finite N and cannot earn assessment credit.
const PendingSearchTotal int64 = -1

// AssessmentPlan supplements v1 without rewriting its canonical bytes. All
// selection, cost, benchmark and sample minima remain pinned by Declaration.
type AssessmentPlan struct {
	ID, DeclarationID, Policy    string
	TrainEnd, HoldoutStart       time.Time
	PurgeSeconds, EmbargoSeconds int64
	AlphaBasisPoints             int64
}

func (p AssessmentPlan) Validate(d Declaration, at time.Time) error {
	if !strategytrial.ValidID(p.ID) || len(p.ID) > 64 || p.DeclarationID != d.ID || p.Policy != AssessmentPolicy || !at.Before(d.WindowStart) || p.TrainEnd.Before(d.WindowStart) || !p.HoldoutStart.Before(d.WindowEnd) || p.PurgeSeconds < d.MaxHoldingSeconds || p.EmbargoSeconds < d.MaxHoldingSeconds || p.PurgeSeconds > MaxSeconds || p.EmbargoSeconds > MaxSeconds || p.HoldoutStart.Before(p.TrainEnd.Add(time.Duration(p.PurgeSeconds+p.EmbargoSeconds)*time.Second)) || p.AlphaBasisPoints < 1 || p.AlphaBasisPoints > 1000 {
		return ErrInvalid
	}
	return nil
}

// AssessmentPlanRecord retains the original clock and complete declaration hash.
type AssessmentPlanRecord struct {
	Plan                      AssessmentPlan
	DeclaredAt                time.Time
	DeclarationSHA256, SHA256 string
}

// BenchmarkObservation is an observed total-return index, including cash yield
// or SPY distributions. Raw retains the original collector statement; its digest
// and original knowledge clocks are never replaced by an assessment timestamp.
// This is source input, not a return or qualification assertion.
type BenchmarkObservation struct {
	StrategyID                         string `json:"strategy_id"`
	Index, EvidenceClass, SourceSHA256 string
	ObservedAt, ReceivedAt             time.Time
	Raw                                []byte
}

func (b BenchmarkObservation) Validate(at time.Time) error {
	var source struct {
		StrategyID, Index, EvidenceClass string
		ObservedAt, ReceivedAt           time.Time
	}
	if json.Unmarshal(b.Raw, &source) != nil || source.StrategyID != b.StrategyID || source.Index != b.Index || source.EvidenceClass != b.EvidenceClass || !source.ObservedAt.Equal(b.ObservedAt) || !source.ReceivedAt.Equal(b.ReceivedAt) {
		return ErrInvalid
	}
	n, ok := new(big.Rat).SetString(b.Index)
	if len(b.Index) > 40 || !decimal.MatchString(b.Index) || !ok || n.Sign() <= 0 || b.EvidenceClass != "prospective_market_unqualified" || b.ObservedAt.IsZero() || b.ReceivedAt.Before(b.ObservedAt) || b.ReceivedAt.After(at) || at.Sub(b.ObservedAt) > time.Minute || len(b.Raw) == 0 || len(b.Raw) > 64<<10 || !json.Valid(b.Raw) || SourceDigest(b.Raw) != b.SourceSHA256 {
		return ErrInvalid
	}
	return nil
}

// Collection identifies new execution under an already retained assessment
// plan. Incubation plans cannot gain this field through an assessment or retry.
type Collection struct {
	// SearchTotal is stamped by the fenced collector, never accepted from an input manifest.
	SearchTotal     *int64          `json:",omitempty"`
	Input           json.RawMessage `json:",omitempty"`
	PlanID          string
	DecisionSources []json.RawMessage `json:",omitempty"`
	Benchmark       BenchmarkObservation
}

// Assessment retains exact inputs, integer sign-test evidence and reasons.
// Empty metrics mean unavailable; successful statistics alone grant no credit.
type Assessment struct {
	PlanID, DeclarationID, StrategyID, SleeveID, Policy                  string
	At                                                                   time.Time
	Status, Reason                                                       string
	PlanSHA256, WindowSHA256, SourceSHA256, JournalSHA256, FamilySHA256  string
	JournalVersion                                                       int64
	SearchStarts                                                         []strategytrial.Start
	SearchTotal                                                          int64
	Sessions, IndependentDecisions, PositiveDays                         int
	PValue, AdjustedPValue, NetExcess, MaximumDrawdown, Fees, CashEffect string
	NetReturn, BenchmarkReturn, ModelFees                                string
	FillSHA256                                                           []string
	QualificationCredit                                                  bool
	SHA256                                                               string
}

type AssessmentStore interface {
	DeclareForwardAssessment(context.Context, AssessmentPlan, func() time.Time) (AssessmentPlanRecord, error)
	ForwardAssessmentPlan(context.Context, string) (AssessmentPlanRecord, error)
	AssessForward(context.Context, string, func() time.Time) (Assessment, error)
}

// SourceDigest hashes original source bytes without JSON normalization.
func SourceDigest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
