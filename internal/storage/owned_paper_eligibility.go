package storage

import (
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperstrategy"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Comparison v15 is the first owner record that can reserve capital. It keeps
// the deferral family name so every earlier version check still applies.
const OwnedAllocationGrantSchema = "mantik-owned-allocation-deferral-v15"
const ownedAllocationGrantAuthority = "service_owned_paper_allocation_reservation_only"
const OwnedEligibilityReadinessSchema = "mantik-owned-admission-readiness-v7"

// completeEvaluationFor reports whether a retained assessment completed an
// applicable evaluation of this exact strategy.
func completeEvaluationFor(a PaperEvaluationAssessment, strategyID string) bool {
	return a.Status == "complete" && a.EvaluationComplete && (a.Recipe == PaperStatisticalAssessmentRecipe || a.Recipe == PaperDrawdownAssessmentRecipe) && slices.Contains(a.CompleteStrategyIDs, strategyID) && !a.AdmissionAvailable && !a.ExecutionAvailable && !a.QualificationCredit
}

// ownedEvaluationAdmissions issues at most one paper admission per proposed
// strategy. It reads only owner-replayed declarations and assessments as of the
// comparison clock. A declaration counts when it names this policy and the
// exact registered strategy record. For each such declaration only its latest
// assessment matters, so a later blocked assessment withdraws the grant and an
// older complete one cannot mask it. The admission is valid from the latest
// complete assessment for the declaration's admission TTL. A policy without an
// execution model has no protective funding and receives no admission.
func ownedEvaluationAdmissions(proposals []allocation.Proposal, policy allocation.PolicyRecord, strategies paperstrategy.Catalog, declarations PaperEvaluationDeclarationCatalog, assessments PaperEvaluationAssessmentCatalog, at time.Time) ([]allocation.Admission, error) {
	out := []allocation.Admission{}
	if policy.Command.ExecutionModel == nil {
		return out, nil
	}
	ids := []string{}
	for _, p := range proposals {
		if !slices.Contains(ids, p.StrategyID) {
			ids = append(ids, p.StrategyID)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		strategy, err := paperstrategy.Select(strategies, id)
		if err != nil {
			return nil, ErrOwnedAllocationConflict
		}
		var best *PaperEvaluationAssessment
		var bestUntil time.Time
		blocked := false
		for _, d := range declarations.Records {
			if d.DeclaredAt.After(at) || d.Command.PolicyID != policy.Command.Policy.ID || d.Command.PolicySHA256 != policy.SHA256 || !slices.ContainsFunc(d.Command.Plan.Variants, func(v PaperEvaluationVariant) bool { return v.ID == id && v.SHA256 == strategy.SHA256 }) {
				continue
			}
			var latest *PaperEvaluationAssessment
			for i := range assessments.Records {
				a := &assessments.Records[i]
				if a.Command.DeclarationID != d.Command.ID || a.Command.DeclarationSHA256 != d.SHA256 || a.AssessedAt.After(at) {
					continue
				}
				if latest == nil || a.AssessedAt.After(latest.AssessedAt) || a.AssessedAt.Equal(latest.AssessedAt) && a.Command.ID > latest.Command.ID {
					latest = a
				}
			}
			switch {
			case latest == nil:
				continue
			case latest.Status == "blocked":
				blocked = true
				continue
			case !completeEvaluationFor(*latest, id):
				continue
			}
			until := latest.AssessedAt.Add(time.Duration(d.Command.Plan.AdmissionTTLSeconds) * time.Second)
			if !at.Before(until) {
				continue
			}
			if best == nil || latest.AssessedAt.After(best.AssessedAt) || latest.AssessedAt.Equal(best.AssessedAt) && latest.Command.ID > best.Command.ID {
				best, bestUntil = latest, until
			}
		}
		if blocked || best == nil {
			continue
		}
		out = append(out, allocation.Admission{StrategyID: id, ArtifactSHA256: strategy.DefinitionSHA256, EvidenceSHA256: best.SHA256, ValidFrom: best.AssessedAt.UTC(), ValidUntil: bestUntil.UTC()})
	}
	return out, nil
}

// ownedReservationExpiry is the owner's receipt for releasing a reservation
// whose proposal expired before any effect was recorded against it.
type ownedReservationExpiry struct {
	Kind           string    `json:"kind"`
	ProposalID     string    `json:"proposal_id"`
	DecisionSHA256 string    `json:"decision_sha256"`
	ValidUntil     time.Time `json:"valid_until"`
	ReservedCents  int64     `json:"reserved_cents"`
}

// ownedReservationExpirations derives the release effects due at a comparison
// clock: every reserved remainder whose proposal is no longer valid and whose
// outcome is not unknown. They are applied before the comparison, in the
// ledger's reservation order, so released cash can fund the new proposals.
func ownedReservationExpirations(p allocation.LedgerProjection, at time.Time) []allocation.Effect {
	out := []allocation.Effect{}
	for _, r := range p.Reservations {
		if r.ReservedCents <= 0 || r.Unknown || at.Before(r.Proposal.ValidUntil) {
			continue
		}
		h := workerplan.Hash(ownedReservationExpiry{Kind: "owner_reservation_expiry", ProposalID: r.Proposal.ID, DecisionSHA256: r.Proposal.DecisionSHA256, ValidUntil: r.Proposal.ValidUntil.UTC(), ReservedCents: r.ReservedCents})
		out = append(out, allocation.Effect{ID: "expiry-" + h[:32], SHA256: h, DecisionID: r.Proposal.ID, Kind: "release", AmountCents: r.ReservedCents})
	}
	return out
}

// applyOwnedEligibility upgrades bound readiness to v7. Eligibility is the
// owner's paper admission for this strategy at the comparison clock, an
// unexpired proposal and bound execution safety. Qualification credit stays
// false: it needs the natural qualification window, which eligibility does not.
func applyOwnedEligibility(c *OwnedAdmissionReadiness, r OwnedAllocationRecord, latest map[string]PaperEvaluationAssessment) {
	c.Schema = OwnedEligibilityReadinessSchema
	for _, a := range latest {
		if completeEvaluationFor(a, c.StrategyID) {
			c.EvaluationComplete = true
		}
	}
	admitted := r.Entry.Command.Comparison != nil && slices.ContainsFunc(r.Entry.Command.Comparison.Admissions, func(a allocation.Admission) bool {
		return a.StrategyID == c.StrategyID && !r.At.Before(a.ValidFrom) && r.At.Before(a.ValidUntil)
	})
	safe := slices.ContainsFunc(r.ExecutionSafety, func(s OwnedExecutionSafety) bool {
		return s.DecisionID == c.DecisionID && s.DecisionSHA256 == c.DecisionSHA256
	})
	// execution_reconciliation stays unavailable: no execution owner records
	// effects yet. Eligibility needs the bound cost ceiling and safety instead.
	for j := range c.Gates {
		g := &c.Gates[j]
		if g.Name == "evaluation_recipe" && c.EvaluationComplete && g.Status == "unavailable" {
			g.Status, g.ReasonCode = "verified", "retained_complete_applicable_evaluation"
		}
	}
	// A failed or pending evaluation gate, from any bound declaration, wins.
	evaluated := slices.ContainsFunc(c.Gates, func(g OwnedAdmissionGate) bool { return g.Name == "evaluation_recipe" && g.Status == "verified" })
	c.PaperOperationallyEligible = admitted && safe && evaluated && c.Status != "expired"
	if c.PaperOperationallyEligible {
		c.Status, c.ReconsiderTriggers = "eligible", []string{"admission_expiry"}
	}
	c.AdmissionAvailable = c.PaperOperationallyEligible
}
