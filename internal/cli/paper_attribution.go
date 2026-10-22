package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

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
	if c.Schema != "mantik-owned-paper-attribution-v1" || c.ReceiverID != *receiver || !paperStrategyPin.MatchString(c.SourceSHA256) || c.Authority != "service_owned_phase_attribution_only" || c.ExecutionAvailable || c.QualificationCredit || c.Rows == nil || len(c.Rows) > storage.MaxPaperDecisions || !paperStrategyPin.MatchString(hash) || workerplan.Hash(c) != hash {
		return storage.ErrPaperAttributionInvalid
	}
	previous := ""
	for _, row := range c.Rows {
		if validateAttributionRow(row, *receiver) != nil || row.Decision.Command.ID <= previous {
			return storage.ErrPaperAttributionInvalid
		}
		previous = row.Decision.Command.ID
	}
	c.SHA256 = hash
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
	if r.Decision.Validate() != nil || r.Decision.Command.ReceiverID != receiver || r.Method == "" || r.Symbol == "" || !paperStrategyPin.MatchString(r.DefinitionSHA256) || r.ForecastStatus != "native_predicate_not_calibrated_forecast" || r.QualificationCredit || r.Observations == nil || len(r.Observations) > storage.MaxPaperOutcomes || r.Allocations == nil || len(r.Allocations) > storage.MaxOwnedAllocations || r.Economics.Status != "owned_execution_unavailable" || r.Economics.BaselineStatus != "economic_baselines_unavailable" || r.Economics.ExecutedQuantity != nil || r.Economics.FeesCents != nil || r.Economics.RealizedPnLCents != nil || !paperStrategyPin.MatchString(hash) || workerplan.Hash(r) != hash {
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
		if !paperaccount.ID.MatchString(a.ComparisonID) || !paperStrategyPin.MatchString(a.RecordSHA256) || !paperStrategyPin.MatchString(a.PolicySHA256) || !paperaccount.ID.MatchString(a.AccountID) || a.At.IsZero() || a.Status != "defer" && a.Status != "reject" || len(a.Reasons) == 0 || a.RequestedFundingCents <= 0 {
			return storage.ErrPaperAttributionInvalid
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
	return nil
}

func renderPaperAttribution(out io.Writer, receiver string, rows []storage.PaperAttributionRow) error {
	// Build the complete output before writing so malformed rows never yield a
	// partial success display. Quote source strings to contain terminal controls.
	var b strings.Builder
	fmt.Fprintf(&b, "RECEIVER %s\nQUALIFICATION false  EXECUTION unavailable\n", strconv.Quote(receiver))
	if len(rows) == 0 {
		fmt.Fprintln(&b, "No finalized decisions retained; insufficient evidence.")
	}
	for _, r := range rows {
		d := r.Decision
		fmt.Fprintf(&b, "DECISION %s  STRATEGY %s  SYMBOL %s\n  action=%s reason=%s evidence=%s\n  forecast=%s observations=%s\n", strconv.Quote(d.Command.ID), strconv.Quote(d.Command.StrategyID), strconv.Quote(r.Symbol), strconv.Quote(d.Evaluation.Action), strconv.Quote(d.Evaluation.ReasonCode), strconv.Quote(r.EvidenceClass), strconv.Quote(r.ForecastStatus), strconv.Quote(r.ObservationStatus))
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
			if a.CostCeiling != nil {
				fmt.Fprintf(&b, "    debit_ceiling_cents=%d (funding bound, not incurred expense)\n", a.CostCeiling.MaxDebitCents)
			}
		}
		fmt.Fprintf(&b, "  ECONOMICS %s executed_quantity=unavailable fees=unavailable realized_pnl=unavailable\n  BASELINES %s\n  ROW SHA256 %s\n", strconv.Quote(r.Economics.Status), strconv.Quote(r.Economics.BaselineStatus), r.SHA256)
	}
	fmt.Fprintln(&b, "Assessments may repeat one decision; no independent sample count or performance rate is inferred. Use --format json for complete evidence and pins.")
	_, err := io.WriteString(out, b.String())
	return err
}
