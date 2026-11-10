package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func runPaperAttribution(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("paper-attribution", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	receiver := fs.String("receiver-id", "", "expected service receiver")
	id := fs.String("id", "", "optional finalized decision ID")
	pin := fs.String("record-sha256", "", "required row SHA-256 when selecting an ID")
	format := fs.String("format", "table", "table or json (complete retained source links)")
	analysis := fs.Bool("analysis", false, "summarize exact strategy outcomes, retained evaluation applicability and unique social/copy source delays")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !storage.ValidReceiverID(*receiver) || (*format != "table" && *format != "json") || (*id == "" && *pin != "") || (*id != "" && (!paperaccount.ID.MatchString(*id) || !paperStrategyPin.MatchString(*pin))) {
		return storage.ErrPaperAttributionInvalid
	}
	path := "/v1/paper-attribution"
	if *id != "" {
		var row storage.PaperAttributionRow
		if err := requestBounded(ctx, *base, path+"/"+*id, nil, &row, nil, storage.MaxPaperAttributionBytes, true); err != nil {
			return err
		}
		if validateAttributionRow(row, *receiver) != nil || row.Decision.Command.ID != *id || row.SHA256 != *pin {
			return storage.ErrPaperAttributionInvalid
		}
		if *analysis {
			return writeAttributionAnalysis(out, *receiver, []storage.PaperAttributionRow{row}, *format)
		}
		if *format == "json" {
			return emit(out, row)
		}
		return renderPaperAttribution(out, *receiver, []storage.PaperAttributionRow{row})
	}
	var c storage.PaperAttributionCatalog
	if err := requestBounded(ctx, *base, path, nil, &c, nil, storage.MaxPaperAttributionBytes, true); err != nil {
		return err
	}
	hash := c.SHA256
	c.SHA256 = ""
	if (c.Schema != "mantik-owned-paper-attribution-v1" && c.Schema != "mantik-owned-paper-attribution-v2" && c.Schema != "mantik-owned-paper-attribution-v3" && c.Schema != "mantik-owned-paper-attribution-v4" && c.Schema != "mantik-owned-paper-attribution-v5") || c.ReceiverID != *receiver || !paperStrategyPin.MatchString(c.SourceSHA256) || c.Authority != "service_owned_phase_attribution_only" || c.ExecutionAvailable || c.QualificationCredit || c.Rows == nil || len(c.Rows) > storage.MaxPaperDecisions || !paperStrategyPin.MatchString(hash) || workerplan.Hash(c) != hash {
		return storage.ErrPaperAttributionInvalid
	}
	previous := ""
	claimReviews := map[string]bool{}
	for _, row := range c.Rows {
		if validateAttributionRow(row, *receiver) != nil || row.Decision.Command.ID <= previous || c.Schema != "mantik-owned-paper-attribution-v1" && row.AdmissionReviewHistory == nil || (c.Schema == "mantik-owned-paper-attribution-v3" || c.Schema == "mantik-owned-paper-attribution-v4" || c.Schema == "mantik-owned-paper-attribution-v5") != (row.ClaimReviewHistory != nil) || c.Schema != "mantik-owned-paper-attribution-v4" && c.Schema != "mantik-owned-paper-attribution-v5" && row.Economics.Status != "owned_execution_unavailable" {
			return storage.ErrPaperAttributionInvalid
		}
		previous = row.Decision.Command.ID
		// A v4 or later row with a reservation reports its execution.
		if (c.Schema == "mantik-owned-paper-attribution-v4" || c.Schema == "mantik-owned-paper-attribution-v5") && row.Economics.Status == "owned_execution_unavailable" {
			for _, a := range row.Allocations {
				if a.Status == "reserve_plan" {
					return storage.ErrPaperAttributionInvalid
				}
			}
		}
		if h := row.ClaimReviewHistory; h != nil {
			for _, review := range h.Records {
				if claimReviews[review.Command.ID] || len(claimReviews) >= storage.MaxDecisionReviews {
					return storage.ErrPaperAttributionInvalid
				}
				claimReviews[review.Command.ID] = true
			}
		}
	}
	c.SHA256 = hash
	if *analysis {
		return writeAttributionAnalysis(out, *receiver, c.Rows, *format)
	}
	if *format == "json" {
		return emit(out, c)
	}
	return renderPaperAttribution(out, *receiver, c.Rows)
}

// Envelope checks protect presentation; the service alone replays source owners.
// Hashes bind the received representation and do not establish qualification.
func validateAttributionRow(r storage.PaperAttributionRow, receiver string) error {
	hash := r.SHA256
	r.SHA256 = ""
	if r.Decision.Validate() != nil || r.Decision.Command.ReceiverID != receiver || r.Method == "" || r.Symbol == "" || !paperStrategyPin.MatchString(r.DefinitionSHA256) || r.ForecastStatus != "native_predicate_not_calibrated_forecast" || r.QualificationCredit || r.Observations == nil || len(r.Observations) > storage.MaxPaperOutcomes || r.Allocations == nil || len(r.Allocations) > storage.MaxOwnedAllocations || validateAttributionEconomics(r) != nil || !paperStrategyPin.MatchString(hash) || workerplan.Hash(r) != hash {
		return storage.ErrPaperAttributionInvalid
	}
	switch r.EvidenceClass {
	case "synthetic_unqualified", "recorded_market_unqualified", "prospective_market_unqualified":
	default:
		return storage.ErrPaperAttributionInvalid
	}
	expected := "not_assessed"
	if len(r.Observations) > 0 {
		expected = "assessments_retained"
	}
	if r.ObservationStatus != expected {
		return storage.ErrPaperAttributionInvalid
	}
	previous := ""
	for _, o := range r.Observations {
		if o.Validate() != nil || o.Command.ReceiverID != receiver || o.Command.DecisionID != r.Decision.Command.ID || o.Command.DecisionSHA256 != r.Decision.SHA256 || o.Command.ID <= previous {
			return storage.ErrPaperAttributionInvalid
		}
		previous = o.Command.ID
	}
	for _, a := range r.Allocations {
		if !paperaccount.ID.MatchString(a.ComparisonID) || !paperStrategyPin.MatchString(a.RecordSHA256) || !paperStrategyPin.MatchString(a.PolicySHA256) || !paperaccount.ID.MatchString(a.AccountID) || a.At.IsZero() || a.Status != "defer" && a.Status != "reject" && a.Status != "reserve_plan" || a.Status != "reserve_plan" && len(a.Reasons) == 0 || a.RequestedFundingCents <= 0 {
			return storage.ErrPaperAttributionInvalid
		}
		if a.AdmissionReadiness != nil && validateAttributionReadiness(*a.AdmissionReadiness, a, r, receiver) != nil {
			return storage.ErrPaperAttributionInvalid
		}
		if c := a.EvaluationContext; c != nil {
			b := c.Binding
			legacy := c.Schema == "mantik-paper-attribution-evaluation-context-v1" && c.Authority == "service_owned_predeclarations_only" && c.AssessmentSourceSHA256 == "" && len(b.Assessments) == 0
			current := c.Schema == "mantik-paper-attribution-evaluation-context-v2" && c.Authority == "service_owned_incomplete_assessments_and_predeclarations_only" && paperStrategyPin.MatchString(c.AssessmentSourceSHA256)
			if (!legacy && !current) || c.AdmissionAvailable || c.ExecutionAvailable || c.QualificationCredit || !paperStrategyPin.MatchString(c.SourceSHA256) || b.DecisionID != r.Decision.Command.ID || b.DecisionSHA256 != r.Decision.SHA256 || b.StrategyID != r.Decision.Command.StrategyID || b.StrategySHA256 != r.Decision.Command.StrategySHA256 || b.PolicySHA256 != a.PolicySHA256 || a.AdmissionReadiness == nil || a.AdmissionReadiness.Binding == nil || b.PolicyID != a.AdmissionReadiness.Binding.PolicyID || b.Declarations == nil || len(b.Declarations) > storage.MaxPaperEvaluationDeclarations {
				return storage.ErrPaperAttributionInvalid
			}
			status := "declaration_unavailable"
			if len(b.Declarations) > 0 {
				status = "declared_not_evaluated"
			}
			if len(b.Assessments) > 0 {
				if !current || len(b.Assessments) > storage.MaxPaperEvaluationAssessments {
					return storage.ErrPaperAttributionInvalid
				}
				status = "assessed_not_admitted"
				previous := ""
				for _, pin := range b.Assessments {
					if !paperaccount.ID.MatchString(pin.ID) || pin.ID <= previous || !paperStrategyPin.MatchString(pin.SHA256) || (pin.Status != "pending" && pin.Status != "blocked" && pin.Status != "incomplete" && pin.Status != "complete") {
						return storage.ErrPaperAttributionInvalid
					}
					found := false
					for _, d := range b.Declarations {
						if d.ID == pin.DeclarationID && d.SHA256 == pin.DeclarationSHA256 {
							found = true
							break
						}
					}
					if !found {
						return storage.ErrPaperAttributionInvalid
					}
					previous = pin.ID
				}
			}
			if b.Status != status {
				return storage.ErrPaperAttributionInvalid
			}
			previous := ""
			for _, pin := range b.Declarations {
				if !paperaccount.ID.MatchString(pin.ID) || pin.ID <= previous || !paperStrategyPin.MatchString(pin.SHA256) {
					return storage.ErrPaperAttributionInvalid
				}
				previous = pin.ID
			}
		}
		if a.ExecutionSafety != nil {
			// Safety requirements and admission readiness describe the same
			// owner-selected account. A self-consistent hash alone is insufficient.
			if a.AdmissionReadiness == nil || a.AdmissionReadiness.Binding == nil || a.ExecutionSafety.AccountSHA256 != a.AdmissionReadiness.Binding.AccountSHA256 {
				return storage.ErrPaperAttributionInvalid
			}
			v := *a.ExecutionSafety
			h := v.SHA256
			v.SHA256 = ""
			c := a.CostCeiling
			if h != workerplan.Hash(v) || !paperStrategyPin.MatchString(h) || v.Schema != "mantik-owned-execution-safety-v1" || v.Authority != "service_owned_execution_requirements_only" || v.AdmissionAvailable || v.ExecutionAvailable || v.QualificationCredit || c == nil || v.ComparisonID != a.ComparisonID || v.DecisionID != r.Decision.Command.ID || v.DecisionSHA256 != r.Decision.SHA256 || v.StrategySHA256 != r.Decision.Command.StrategySHA256 || v.DefinitionSHA256 != r.DefinitionSHA256 || v.BasisSHA256 != r.Decision.BasisSHA256 || v.PolicySHA256 != a.PolicySHA256 || !paperStrategyPin.MatchString(v.AccountSHA256) || v.CostCeilingSHA256 != c.SHA256 || v.ModelSHA256 != c.ModelSHA256 || v.QuoteInputID != c.QuoteInputID || !v.FinalizedAt.Equal(r.Decision.FinalizedAt) || !v.ProposalValidUntil.Equal(c.ValidUntil) || v.Quantity != c.Quantity || v.EntryLimitCents != c.LimitCents || v.Safety.StopCents < 1 || v.Safety.StopCents >= c.ReferencePriceCents || v.Safety.TargetCents <= c.ReferencePriceCents || v.Safety.MaxHoldingSeconds < 1 || v.Safety.MaxHoldingSeconds > 86400 || v.Safety.MaxQuoteAgeSeconds < 1 || v.Safety.MaxQuoteAgeSeconds > 3600 || v.Safety.MaxNotionalCents < c.GrossCeilingCents {
				return storage.ErrPaperAttributionInvalid
			}
		}
		if a.CostCeiling != nil {
			cost := *a.CostCeiling
			h := cost.SHA256
			cost.SHA256 = ""
			if cost.DecisionID != r.Decision.Command.ID || cost.DecisionSHA256 != r.Decision.SHA256 || !paperStrategyPin.MatchString(h) || workerplan.Hash(cost) != h {
				return storage.ErrPaperAttributionInvalid
			}
		}
	}
	if r.ClaimReviewHistory != nil && r.ClaimReviewHistory.Validate(r.Decision) != nil {
		return storage.ErrPaperAttributionInvalid
	}
	return validateAttributionReviews(r, receiver)
}

// Optional for older retained comparisons. A portable readiness hash does not
// attest the source owner; only the service replays the underlying journal.
func validateAttributionReadiness(c storage.OwnedAdmissionReadiness, a storage.PaperAllocationAttribution, r storage.PaperAttributionRow, receiver string) error {
	h := c.SHA256
	c.SHA256 = ""
	if !paperStrategyPin.MatchString(h) || workerplan.Hash(c) != h || c.ReceiverID != receiver || c.DecisionID != r.Decision.Command.ID || c.DecisionSHA256 != r.Decision.SHA256 || c.StrategyID != r.Decision.Command.StrategyID || c.StrategySHA256 != r.Decision.Command.StrategySHA256 || c.DefinitionSHA256 != r.DefinitionSHA256 || c.BasisSHA256 != r.Decision.BasisSHA256 || c.EvidenceClass != r.EvidenceClass || !c.CheckedAt.Equal(a.At) || c.Authority != "service_owned_admission_readiness_only" || c.ExecutionAvailable || c.QualificationCredit || c.Status != "deferred" && c.Status != "expired" && c.Status != "eligible" || c.Gates == nil || c.ReconsiderTriggers == nil {
		return storage.ErrPaperAttributionInvalid
	}
	expectedStatus := "deferred"
	triggers := []string{"qualified_source_evidence", "applicable_evaluation_receipt", "owned_execution_evidence"}
	if !c.CheckedAt.Before(c.ValidUntil) {
		expectedStatus = "expired"
		triggers = []string{"new_finalized_decision"}
	}
	gates := []storage.OwnedAdmissionGate{
		{Name: "finalized_decision", Status: "verified", ReasonCode: "retained_owner_decision"},
		{Name: "source_evidence_class", Status: "unqualified", ReasonCode: r.EvidenceClass},
		{Name: "evaluation_recipe", Status: "unavailable", ReasonCode: "relevant_evaluation_receipt_unavailable"},
		{Name: "execution_reconciliation", Status: "unavailable", ReasonCode: "owned_execution_evidence_unavailable"},
	}
	if c.Schema == "mantik-owned-admission-readiness-v3" || (c.Schema == "mantik-owned-admission-readiness-v4" || (c.Schema == "mantik-owned-admission-readiness-v5" || c.Schema == "mantik-owned-admission-readiness-v6" || c.Schema == "mantik-owned-admission-readiness-v7")) {
		gate, err := validateReadinessAssessmentSelection(c, a)
		if err != nil {
			return err
		}
		gates[2] = gate
	} else if len(c.EvaluationEvidence) > 0 || c.Binding != nil && c.Binding.EvaluationBindingSHA256 != "" {
		return storage.ErrPaperAttributionInvalid
	}
	if c.Schema == "mantik-owned-admission-readiness-v7" {
		// The service replays the admission itself; a client checks that the
		// retained eligibility agrees with the readiness it was derived from.
		if c.EvaluationComplete && gates[2].Status == "unavailable" {
			gates[2] = storage.OwnedAdmissionGate{Name: "evaluation_recipe", Status: "verified", ReasonCode: "retained_complete_applicable_evaluation"}
		}
		if c.PaperOperationallyEligible {
			if !c.EvaluationComplete || expectedStatus == "expired" || a.ExecutionSafety == nil || gates[2].Status != "verified" {
				return storage.ErrPaperAttributionInvalid
			}
			expectedStatus, triggers = "eligible", []string{"admission_expiry"}
		}
		if c.AdmissionAvailable != c.PaperOperationallyEligible || c.ExecutionAvailable || c.QualificationCredit {
			return storage.ErrPaperAttributionInvalid
		}
	} else if c.EvaluationComplete || c.PaperOperationallyEligible || c.AdmissionAvailable {
		return storage.ErrPaperAttributionInvalid
	}
	if c.QuoteInputID != r.Decision.Evaluation.Coverage.Quote.InputID || c.ValidUntil.IsZero() || c.Status != expectedStatus || !reflect.DeepEqual(c.Gates, gates) || !reflect.DeepEqual(c.ReconsiderTriggers, triggers) {
		return storage.ErrPaperAttributionInvalid
	}
	switch c.Schema {
	case "mantik-owned-admission-readiness-v1":
		if c.Binding != nil {
			return storage.ErrPaperAttributionInvalid
		}
	case "mantik-owned-admission-readiness-v2", "mantik-owned-admission-readiness-v3", "mantik-owned-admission-readiness-v4", "mantik-owned-admission-readiness-v5", "mantik-owned-admission-readiness-v6", "mantik-owned-admission-readiness-v7":
		b := c.Binding
		if b == nil || b.ComparisonID != a.ComparisonID || b.PolicySHA256 != a.PolicySHA256 || b.AccountID != a.AccountID || !paperaccount.ID.MatchString(b.PolicyID) || !paperStrategyPin.MatchString(b.AccountSHA256) || !paperStrategyPin.MatchString(b.ProposalSHA256) || !paperStrategyPin.MatchString(b.LedgerSHA256) {
			return storage.ErrPaperAttributionInvalid
		}
		expectedCost := ""
		if a.CostCeiling != nil {
			expectedCost = a.CostCeiling.SHA256
		}
		expectedSafety := ""
		if (c.Schema == "mantik-owned-admission-readiness-v5" || c.Schema == "mantik-owned-admission-readiness-v6" || c.Schema == "mantik-owned-admission-readiness-v7") && a.ExecutionSafety != nil {
			expectedSafety = a.ExecutionSafety.SHA256
		}
		if b.ExecutionSafetySHA256 != expectedSafety {
			return storage.ErrPaperAttributionInvalid
		}
		if b.ExecutionCostSHA256 != expectedCost {
			return storage.ErrPaperAttributionInvalid
		}
	default:
		return storage.ErrPaperAttributionInvalid
	}
	return nil
}

// validateAttributionEconomics checks that a row's execution attribution is
// internally consistent: its sums match its effects, its status follows from
// them, and nothing is inferred that the ledger does not retain.
func validateAttributionEconomics(r storage.PaperAttributionRow) error {
	e := r.Economics
	if e.ExecutedQuantity != nil || e.FeesCents != nil {
		return storage.ErrPaperAttributionInvalid
	}
	var reserved int64
	for _, a := range r.Allocations {
		if a.Status == "reserve_plan" {
			reserved += a.RequestedFundingCents
		}
	}
	if e.Status == "owned_execution_unavailable" {
		if e.RealizedPnLCents != nil || e.ReservedCents != nil || e.FilledCostCents != nil || e.ClosedCostCents != nil || e.NetProceedsCents != nil || e.ReleasedCents != nil || e.Effects != nil {
			return storage.ErrPaperAttributionInvalid
		}
		return nil
	}
	if e.ReservedCents == nil || e.FilledCostCents == nil || e.ClosedCostCents == nil || e.NetProceedsCents == nil || e.ReleasedCents == nil || e.Effects == nil || *e.ReservedCents != reserved || reserved <= 0 || len(e.Effects) > allocation.MaxLedgerEntries {
		return storage.ErrPaperAttributionInvalid
	}
	var filled, closed, proceeds, released int64
	var previous time.Time
	for _, x := range e.Effects {
		if !paperaccount.ID.MatchString(x.RecordID) || x.EffectID == "" || x.At.IsZero() || x.At.Before(previous) || x.AmountCents < 0 {
			return storage.ErrPaperAttributionInvalid
		}
		previous = x.At
		switch x.Kind {
		case "fill":
			filled += x.AmountCents
		case "close":
			closed += x.AmountCents
			proceeds += x.ProceedsCents
		case "release":
			released += x.AmountCents
		default:
			return storage.ErrPaperAttributionInvalid
		}
		if x.Kind != "close" && x.ProceedsCents != 0 {
			return storage.ErrPaperAttributionInvalid
		}
	}
	status := "position_closed"
	switch {
	case filled == 0 && released == 0:
		status = "reserved_unfilled"
	case filled == 0:
		status = "released_unfilled"
	case closed < filled:
		status = "position_open"
	}
	if *e.FilledCostCents != filled || *e.ClosedCostCents != closed || *e.NetProceedsCents != proceeds || *e.ReleasedCents != released || e.Status != status || closed > filled || filled+released > reserved {
		return storage.ErrPaperAttributionInvalid
	}
	if (closed > 0) != (e.RealizedPnLCents != nil) || e.RealizedPnLCents != nil && *e.RealizedPnLCents != proceeds-closed {
		return storage.ErrPaperAttributionInvalid
	}
	return validateAttributionBaseline(r)
}

// validateAttributionBaseline rejects a baseline the row's own evidence does
// not produce. Rows from before v5 carry the old fixed status and no figures.
func validateAttributionBaseline(r storage.PaperAttributionRow) error {
	e := r.Economics
	if e.BaselineStatus == "economic_baselines_unavailable" && e.Baseline == nil {
		return nil
	}
	status, baseline := storage.PaperAttributionBaseline(e, r.Observations)
	if status != e.BaselineStatus || !reflect.DeepEqual(baseline, e.Baseline) {
		return storage.ErrPaperAttributionInvalid
	}
	return nil
}

// renderBaseline formats the row's baseline figures, naming each absent one.
func renderBaseline(e storage.PaperEconomicAttribution) string {
	number := func(v *int64) string {
		if v == nil {
			return "unavailable"
		}
		return strconv.FormatInt(*v, 10)
	}
	b := e.Baseline
	if b == nil {
		return strconv.Quote(e.BaselineStatus)
	}
	return fmt.Sprintf("%s passive_return_bps=%s strategy_return_bps=%s excess_over_passive_bps=%s excess_over_no_action_bps=%s (no-action is cash; passive runs over the forward horizon)", strconv.Quote(e.BaselineStatus), number(b.PassiveReturnBPS), number(b.StrategyReturnBPS), number(b.ExcessOverPassiveBPS), number(b.ExcessOverNoActionBPS))
}

func renderPaperAttribution(out io.Writer, receiver string, rows []storage.PaperAttributionRow) error {
	// Build the complete output before writing so malformed rows never yield a
	// partial success display. Quote source strings to contain terminal controls.
	var b strings.Builder
	execution := "unavailable"
	for _, r := range rows {
		if r.Economics.FilledCostCents != nil {
			execution = "paper"
		}
	}
	fmt.Fprintf(&b, "RECEIVER %s\nQUALIFICATION false  EXECUTION %s\n", strconv.Quote(receiver), execution)
	if len(rows) == 0 {
		fmt.Fprintln(&b, "No finalized decisions retained; insufficient evidence.")
	}
	for _, r := range rows {
		d := r.Decision
		fmt.Fprintf(&b, "DECISION %s  STRATEGY %s  SYMBOL %s\n  action=%s reason=%s evidence=%s\n  forecast=%s observations=%s\n", strconv.Quote(d.Command.ID), strconv.Quote(d.Command.StrategyID), strconv.Quote(r.Symbol), strconv.Quote(d.Evaluation.Action), strconv.Quote(d.Evaluation.ReasonCode), strconv.Quote(r.EvidenceClass), strconv.Quote(r.ForecastStatus), strconv.Quote(r.ObservationStatus))
		fmt.Fprintf(&b, "  SIGNAL stored_predicate=%t probability_scoring=not_applicable (native threshold has no probability forecast)\n", d.Evaluation.Signal)
		if len(r.Observations) == 0 {
			fmt.Fprintln(&b, "  OUTCOME_EVIDENCE insufficient: no assessment retained.")
		}
		if d.Evaluation.Action == "abstain" {
			fmt.Fprintln(&b, "  Finalized no-action decision; not a workflow failure.")
		}
		for _, o := range r.Observations {
			delta := "unavailable"
			if o.Evaluation.Status == "observed" {
				delta = strconv.FormatInt(o.Evaluation.PriceDeltaCents, 10)
			}
			fmt.Fprintf(&b, "  ASSESSMENT %s status=%s reason=%s quote_delta_cents=%s (not trading P&L)\n", strconv.Quote(o.Command.ID), strconv.Quote(o.Evaluation.Status), strconv.Quote(o.Evaluation.ReasonCode), delta)
		}
		for _, a := range r.Allocations {
			fmt.Fprintf(&b, "  ALLOCATION %s account=%s status=%s reasons=%q requested_funding_cents=%d\n", strconv.Quote(a.ComparisonID), strconv.Quote(a.AccountID), strconv.Quote(a.Status), a.Reasons, a.RequestedFundingCents)
			if a.AdmissionReadiness != nil {
				c := a.AdmissionReadiness
				fmt.Fprintf(&b, "    ADMISSION_READINESS status=%s admission=false execution=false\n", strconv.Quote(c.Status))
				for _, gate := range c.Gates {
					fmt.Fprintf(&b, "      GATE %s status=%s reason=%s\n", strconv.Quote(gate.Name), strconv.Quote(gate.Status), strconv.Quote(gate.ReasonCode))
				}
			}
			if h := r.AdmissionReviewHistory; h != nil {
				for _, review := range h.Records {
					if review.Command.ComparisonID != a.ComparisonID {
						continue
					}
					fmt.Fprintf(&b, "    ADMISSION_REVIEW %s current=%t status=%s reason=%s reconsider=%q admission=false execution=false qualification=false\n", strconv.Quote(review.Command.ID), review.Command.ID == h.CurrentID, strconv.Quote(review.Status), strconv.Quote(review.ReasonCode), review.ReconsiderTriggers)
				}
			}
			if c := a.EvaluationContext; c != nil {
				fmt.Fprintf(&b, "    EVALUATION_PLANS status=%s source=%s admission=false execution=false qualification=false\n", strconv.Quote(c.Binding.Status), strconv.Quote(c.SourceSHA256))
				for _, pin := range c.Binding.Declarations {
					fmt.Fprintf(&b, "      DECLARATION %s sha256=%s (predeclared plan, not evaluated)\n", strconv.Quote(pin.ID), strconv.Quote(pin.SHA256))
				}
			}
			if a.CostCeiling != nil {
				fmt.Fprintf(&b, "    debit_ceiling_cents=%d (funding bound, not incurred expense)\n", a.CostCeiling.MaxDebitCents)
			}
		}
		if r.ClaimReviewHistory == nil || len(r.ClaimReviewHistory.SourceTimings) == 0 {
			fmt.Fprintln(&b, "  SOURCE_TIMING unavailable: no retained social/copy timing evidence.")
		}
		if h := r.ClaimReviewHistory; h != nil {
			kinds := map[string]string{}
			for _, review := range h.Records {
				if review.Source != nil {
					kinds[review.Command.ID] = review.Source.Kind
				}
				fmt.Fprintf(&b, "  CLAIM_REVIEW %s status=%s case=%s (post-decision review; no admission)\n", strconv.Quote(review.Command.ID), strconv.Quote(review.Status), strconv.Quote(review.CaseID))
			}
			for _, timing := range h.SourceTimings {
				fmt.Fprintf(&b, "    SOURCE_TIMING review=%s capture=%s kind=%s observed_delay_seconds=%d retained_delay_seconds=%d ingestion_delay_seconds=%d relation=%s capture_sha256=%s decision_input_verified=false\n", strconv.Quote(timing.ReviewID), strconv.Quote(timing.CaptureID), strconv.Quote(kinds[timing.ReviewID]), timing.ObservationDelaySeconds, timing.AvailabilityDelaySeconds, int64(timing.RetainedAt.Sub(timing.DeclaredObservedAt)/time.Second), strconv.Quote(timing.ClockRelation), strconv.Quote(timing.CaptureSHA256))
			}
		}
		if e := r.Economics; e.FilledCostCents != nil {
			realized := "unavailable"
			if e.RealizedPnLCents != nil {
				realized = strconv.FormatInt(*e.RealizedPnLCents, 10)
			}
			fmt.Fprintf(&b, "  ECONOMICS %s reserved_cents=%d filled_cost_cents=%d closed_cost_cents=%d net_proceeds_cents=%d released_cents=%d realized_pnl_cents=%s (paper fills; fees inside cost and proceeds; quantity not retained)\n", strconv.Quote(e.Status), *e.ReservedCents, *e.FilledCostCents, *e.ClosedCostCents, *e.NetProceedsCents, *e.ReleasedCents, realized)
			for _, x := range e.Effects {
				fmt.Fprintf(&b, "    EFFECT %s %s at=%s amount_cents=%d proceeds_cents=%d record=%s\n", strconv.Quote(x.Kind), strconv.Quote(x.EffectID), x.At.UTC().Format(time.RFC3339Nano), x.AmountCents, x.ProceedsCents, strconv.Quote(x.RecordID))
			}
			fmt.Fprintf(&b, "  BASELINES %s\n  ROW SHA256 %s\n", renderBaseline(e), r.SHA256)
		} else {
			fmt.Fprintf(&b, "  ECONOMICS %s executed_quantity=unavailable fees=unavailable realized_pnl=unavailable\n  BASELINES %s\n  ROW SHA256 %s\n", strconv.Quote(r.Economics.Status), renderBaseline(r.Economics), r.SHA256)
		}
	}
	fmt.Fprintln(&b, "Assessments may repeat one decision; no independent sample count or performance rate is inferred. Source timings describe post-decision review evidence; causal inputs and copy execution returns remain unverified. Raw JSON retains complete evidence and pins.")
	_, err := io.WriteString(out, b.String())
	return err
}

// Portable shape validation is not an owner attestation or admission grant.
func validateReadinessAssessmentSelection(c storage.OwnedAdmissionReadiness, a storage.PaperAllocationAttribution) (storage.OwnedAdmissionGate, error) {
	gate := storage.OwnedAdmissionGate{Name: "evaluation_recipe", Status: "unavailable", ReasonCode: "relevant_evaluation_receipt_unavailable"}
	if c.Binding == nil || a.EvaluationContext == nil || c.Binding.EvaluationBindingSHA256 != workerplan.Hash(a.EvaluationContext.Binding) || len(c.EvaluationEvidence) > storage.MaxPaperEvaluationAssessments {
		return gate, storage.ErrPaperAttributionInvalid
	}
	previous := ""
	selected, pending, failed := false, false, false
	for _, v := range c.EvaluationEvidence {
		if !paperaccount.ID.MatchString(v.AssessmentID) || !paperaccount.ID.MatchString(v.DeclarationID) || v.DeclarationID <= previous || !paperStrategyPin.MatchString(v.AssessmentSHA256) || !paperStrategyPin.MatchString(v.DeclarationSHA256) || !paperStrategyPin.MatchString(v.EvidenceSHA256) || v.AssessedAt.IsZero() || v.AssessedAt.After(c.CheckedAt) || (v.SampleStatus != "pending" && v.SampleStatus != "failed" && v.SampleStatus != "unavailable" && v.SampleStatus != "passed") {
			return gate, storage.ErrPaperAttributionInvalid
		}
		if err := validateReadinessFailures(c, v); err != nil {
			return gate, err
		}
		found := false
		for _, pin := range a.EvaluationContext.Binding.Assessments {
			if pin.ID == v.AssessmentID && pin.SHA256 == v.AssessmentSHA256 && pin.DeclarationID == v.DeclarationID && pin.DeclarationSHA256 == v.DeclarationSHA256 {
				found = true
				break
			}
		}
		if !found {
			return gate, storage.ErrPaperAttributionInvalid
		}
		if (c.Schema == "mantik-owned-admission-readiness-v6" || c.Schema == "mantik-owned-admission-readiness-v7") && v.DecisionEvidenceStatus == "source_unavailable_at_decision" && len(v.FailedChecks) > 0 {
			failed = true
		}
		switch v.DecisionEvidenceStatus {
		case "selected_research_evidence":
			selected = true
			pending = pending || v.SampleStatus == "pending"
			failed = failed || v.SampleStatus == "failed" || len(v.FailedChecks) > 0
		case "decision_not_retained", "decision_before_window", "decision_after_window", "abstention", "quote_before_window", "quote_after_window", "source_unavailable_at_decision", "synthetic_not_selected", "duplicate_strategy_occurrence":
		default:
			return gate, storage.ErrPaperAttributionInvalid
		}
		previous = v.DeclarationID
	}
	switch {
	case failed:
		gate.Status, gate.ReasonCode = "failed", "retained_necessary_sample_check_failed"
		if c.Schema == "mantik-owned-admission-readiness-v4" || (c.Schema == "mantik-owned-admission-readiness-v5" || c.Schema == "mantik-owned-admission-readiness-v6" || c.Schema == "mantik-owned-admission-readiness-v7") {
			gate.ReasonCode = "retained_necessary_evaluation_check_failed"
		}
	case pending:
		gate.Status, gate.ReasonCode = "pending", "retained_evaluation_outcome_window_open"
	case selected:
		gate.ReasonCode = "retained_evaluation_recipe_incomplete"
	case len(c.EvaluationEvidence) > 0:
		gate.ReasonCode = "finalized_decision_not_selected_in_assessment"
	}
	return gate, nil
}

// Presentation validation checks the complete decision chain against the retained
// comparison projection. It cannot attest storage ownership or grant admission.
func validateAttributionReviews(row storage.PaperAttributionRow, receiver string) error {
	h := row.AdmissionReviewHistory
	if h == nil {
		return nil
	} // Legacy v1 response.
	if h.Records == nil || len(h.Records) > storage.MaxOwnedAdmissionReviews || h.Authority != "service_owned_non_grant_admission_review_history_only" || h.AdmissionAvailable || h.ExecutionAvailable || h.QualificationCredit || !paperStrategyPin.MatchString(h.SourceSHA256) {
		return storage.ErrPaperAttributionInvalid
	}
	scoped := storage.OwnedAdmissionReviewCatalog{ReceiverID: receiver, Records: h.Records, CurrentIDs: []string{}}
	var previous storage.OwnedAdmissionReview
	seen := map[string]bool{}
	for _, review := range h.Records {
		c := review.Command
		raw := review
		raw.SHA256 = ""
		if c.Validate() != nil || c.ReceiverID != receiver || c.DecisionID != row.Decision.Command.ID || c.DecisionSHA256 != row.Decision.SHA256 || !validAttributionReviewKind(review) || review.ExecutionAvailable || review.QualificationCredit || !paperStrategyPin.MatchString(review.SHA256) || workerplan.Hash(raw) != review.SHA256 || seen[c.ID] || review.Sequence <= previous.Sequence || review.Sequence > storage.MaxOwnedAdmissionReviews || review.ReviewedAt.IsZero() || review.ReviewedAt.Before(previous.ReviewedAt) || review.ComparisonAt.Before(previous.ComparisonAt) || review.ReviewedAt.Before(review.ComparisonAt) || c.PreviousID != previous.Command.ID || c.PreviousSHA256 != previous.SHA256 {
			return storage.ErrPaperAttributionInvalid
		}
		seen[c.ID] = true
		found := false
		for _, a := range row.Allocations {
			if a.ComparisonID != c.ComparisonID {
				continue
			}
			if found || a.RecordSHA256 != c.ComparisonSHA256 || !a.At.Equal(review.ComparisonAt) || a.AdmissionReadiness == nil || !reflect.DeepEqual(*a.AdmissionReadiness, review.Readiness) {
				return storage.ErrPaperAttributionInvalid
			}
			found = true
		}
		readiness := review.Readiness
		if !found || (readiness.Schema != "mantik-owned-admission-readiness-v3" && readiness.Schema != "mantik-owned-admission-readiness-v4" && readiness.Schema != "mantik-owned-admission-readiness-v5" && readiness.Schema != "mantik-owned-admission-readiness-v6" && readiness.Schema != "mantik-owned-admission-readiness-v7") || readiness.Binding == nil || (review.Schema == "mantik-owned-admission-review-v2") != (readiness.Schema == "mantik-owned-admission-readiness-v7") {
			return storage.ErrPaperAttributionInvalid
		}
		status, reason := "deferred", "required_admission_gates_unavailable"
		triggers := readiness.ReconsiderTriggers
		failed, pending := false, false
		for _, gate := range readiness.Gates {
			failed = failed || gate.Status == "failed"
			pending = pending || gate.Status == "pending"
		}
		switch {
		case !review.ReviewedAt.Before(readiness.ValidUntil):
			status, reason, triggers = "expired", "finalized_proposal_expired", []string{"new_finalized_decision"}
		case failed:
			status, reason = "blocked", "retained_necessary_evaluation_check_failed"
		case pending:
			status, reason = "pending", "retained_evaluation_window_open"
		case readiness.PaperOperationallyEligible:
			status, reason = "eligible", "owner_issued_paper_eligibility"
		}
		if review.AdmissionAvailable != (status == "eligible") || review.Status != status || review.ReasonCode != reason || !reflect.DeepEqual(review.ReconsiderTriggers, triggers) {
			return storage.ErrPaperAttributionInvalid
		}
		previous = review
	}
	if h.CurrentID != previous.Command.ID {
		return storage.ErrPaperAttributionInvalid
	}
	if h.CurrentID != "" {
		scoped.CurrentIDs = []string{h.CurrentID}
	}
	if workerplan.Hash(scoped) != h.SourceSHA256 {
		return storage.ErrPaperAttributionInvalid
	}
	return nil
}

// These are portable failure shapes, not source attestations. Actual owner replay
// must still rederive the exact failure list from the pinned assessment.
func validateReadinessFailures(c storage.OwnedAdmissionReadiness, v storage.OwnedAssessmentReadiness) error {
	if c.Schema != "mantik-owned-admission-readiness-v4" && c.Schema != "mantik-owned-admission-readiness-v5" && c.Schema != "mantik-owned-admission-readiness-v6" && c.Schema != "mantik-owned-admission-readiness-v7" {
		if len(v.FailedChecks) > 0 {
			return storage.ErrPaperAttributionInvalid
		}
		return nil
	}
	previous := -1
	for _, check := range v.FailedChecks {
		index := -1
		switch check.Name {
		case "leakage":
			if check.ReasonCode == "retained_candidate_source_unavailable_at_finalization" {
				index = 0
			}
		case "sample_sufficiency":
			if check.ReasonCode == "observed_occurrence_upper_bound_below_predeclared_minimum" && v.SampleStatus == "failed" {
				index = 6
			}
		}
		if check.StrategyID != c.StrategyID || check.Status != "failed" || index <= previous {
			return storage.ErrPaperAttributionInvalid
		}
		previous = index
	}
	if (v.SampleStatus == "failed") != (len(v.FailedChecks) > 0 && v.FailedChecks[len(v.FailedChecks)-1].Name == "sample_sufficiency") {
		return storage.ErrPaperAttributionInvalid
	}
	return nil
}

// Review v1 never grants; review v2 reviews owner-issued paper eligibility.
func validAttributionReviewKind(r storage.OwnedAdmissionReview) bool {
	switch r.Schema {
	case "mantik-owned-admission-review-v1":
		return r.Method == "retained-non-grant-admission-review-v1" && r.Authority == "service_owned_non_grant_admission_review_only" && !r.AdmissionAvailable
	case "mantik-owned-admission-review-v2":
		return r.Method == "retained-owner-eligibility-review-v1" && r.Authority == "service_owned_paper_eligibility_review_only"
	}
	return false
}
