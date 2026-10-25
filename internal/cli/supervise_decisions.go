package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type decisionCatalogFetch func(context.Context) (storage.PaperDecisionCatalog, error)
type decisionInspectionFetch func(context.Context, storage.PaperDecisionRecord, string) (any, error)

var errDecisionReadback = errors.New("decision readback does not match the retained owner and decision")

// Validate bindings in addition to the response hash. A hash alone cannot make
// an imported explanation authoritative; these reads use the existing owner API.
func fetchDecisionInspection(ctx context.Context, base string, d storage.PaperDecisionRecord, kind string) (any, error) {
	if d.Validate() != nil {
		return nil, errDecisionReadback
	}
	path := "/v1/paper-decisions/" + d.Command.ID
	switch kind {
	case "a":
		var row storage.PaperAttributionRow
		if err := requestBounded(ctx, base, "/v1/paper-attribution/"+d.Command.ID, nil, &row, nil, storage.MaxPaperAttributionBytes, true); err != nil {
			return nil, err
		}
		if validateAttributionRow(row, d.Command.ReceiverID) != nil || !reflect.DeepEqual(row.Decision, d) {
			return nil, errDecisionReadback
		}
		return row, nil
	case "e":
		var v service.DecisionExplanation
		if err := requestBounded(ctx, base, path+"/explanation", nil, &v, nil, 1<<20, true); err != nil {
			return nil, err
		}
		pin := v.SHA256
		v.SHA256 = ""
		if pin != workerplan.Hash(v) || v.Schema != "mantik-finalized-decision-explanation-v1" || v.Method != "deterministic-retained-decision-facts-v1" || v.Authority != "finalized_research_decision_explanation_only" || v.ModelUsed || v.AdmissionAvailable || v.ExecutionAvailable || v.QualificationCredit || v.ReceiverID != d.Command.ReceiverID || v.DecisionID != d.Command.ID || v.DecisionSHA256 != d.SHA256 || v.BasisSHA256 != d.BasisSHA256 || !v.FinalizedAt.Equal(d.FinalizedAt) || v.StrategyID != d.Command.StrategyID || v.StrategySHA256 != d.Command.StrategySHA256 || v.RegistrationID != d.Command.RegistrationID || v.Action != d.Evaluation.Action || v.ReasonCode != d.Evaluation.ReasonCode || v.ReferencePriceCents != d.Evaluation.PriceCents || v.StoredSignal != d.Evaluation.Signal || !reflect.DeepEqual(v.Coverage, d.Evaluation.Coverage) || v.RecordPath != path || v.Statements == nil {
			return nil, errDecisionReadback
		}
		v.SHA256 = pin
		return v, nil
	case "m":
		var v service.PaperDecisionMarketEvidence
		if err := requestBounded(ctx, base, path+"/market-evidence", nil, &v, nil, 1<<20, true); err != nil {
			return nil, err
		}
		pin := v.SHA256
		v.SHA256 = ""
		if pin != workerplan.Hash(v) || v.Schema != "mantik-owned-decision-market-evidence-v1" || v.Authority != "service_owned_recorded_market_dimensions_only" || v.AdmissionAvailable || v.ExecutionAvailable || v.QualificationCredit || v.ReceiverID != d.Command.ReceiverID || v.DecisionID != d.Command.ID || v.DecisionSHA256 != d.SHA256 || v.BasisSHA256 != d.BasisSHA256 || !v.FinalizedAt.Equal(d.FinalizedAt) || v.RegistrationID != d.Command.RegistrationID {
			return nil, errDecisionReadback
		}
		v.SHA256 = pin
		return v, nil
	}
	return nil, errDecisionReadback
}

const decisionSupervisionHelp = "Commands (then Enter): n next | p previous | r refresh | d ID finalized decision | e ID explanation | m ID market evidence | a ID attribution | q quit"

// No natural-language inference or mutation is performed. IDs must belong to
// the displayed page; stale catalogues cannot authorize follow-up reads.
func superviseDecisions(ctx context.Context, in io.ReadCloser, out io.Writer, limit int, fetch decisionCatalogFetch, inspect decisionInspectionFetch) error {
	scanner, cleanup := supervisionInput(ctx, in)
	defer cleanup()
	if (storage.ActivityQuery{Limit: limit}).Validate() != nil {
		return errDecisionReadback
	}
	var page storage.PaperDecisionCatalog
	owner := ""
	offset := 0
	read, stale, havePage := true, true, false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if read {
			fresh, err := fetch(ctx)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err == nil && (!storage.ValidReceiverID(fresh.ReceiverID) || owner != "" && fresh.ReceiverID != owner || fresh.Records == nil || len(fresh.Records) > storage.MaxPaperDecisions) {
				err = errDecisionReadback
			}
			seen := map[string]bool{}
			for _, d := range fresh.Records {
				if d.Validate() != nil || d.Command.ReceiverID != fresh.ReceiverID || seen[d.Command.ID] {
					err = errDecisionReadback
				}
				seen[d.Command.ID] = true
			}
			stale = err != nil
			if stale {
				if _, err := fmt.Fprintln(out, "STALE: decision catalogue read failed or owner changed. Use r to retry or q to quit; details are unavailable."); err != nil {
					return err
				}
			} else {
				raw, err := json.Marshal(fresh)
				if err != nil {
					return err
				}
				if err = json.Unmarshal(raw, &page); err != nil {
					return err
				}
				owner, offset, havePage = fresh.ReceiverID, 0, true
			}
			read = false
		}
		if havePage {
			label := "READBACK"
			if stale {
				label = "STALE"
			}
			if _, err := fmt.Fprintf(out, "%s FINALIZED RESEARCH DECISIONS | receiver=%q | count=%d\nNo admission, execution or qualification is granted by this view.\n", label, owner, len(page.Records)); err != nil {
				return err
			}
			for _, d := range page.Records[offset:min(offset+limit, len(page.Records))] {
				if _, err := fmt.Fprintf(out, "%s | strategy=%q action=%q reason=%q sha256=%s\n", d.Command.ID, d.Command.StrategyID, d.Evaluation.Action, d.Evaluation.ReasonCode, d.SHA256); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(out, decisionSupervisionHelp); err != nil {
			return err
		}
		if _, err := fmt.Fprint(out, "> "); err != nil {
			return err
		}
		if !scanner.Scan() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return scanner.Err()
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) == 1 && fields[0] == "q" {
			return nil
		}
		if len(fields) == 1 && fields[0] == "r" {
			read = true
			continue
		}
		if stale {
			if _, err := fmt.Fprintln(out, "STALE: refresh successfully before navigating or inspecting decisions."); err != nil {
				return err
			}
			continue
		}
		switch {
		case len(fields) == 1 && fields[0] == "n":
			if offset+limit < len(page.Records) {
				offset += limit
			}
		case len(fields) == 1 && fields[0] == "p":
			offset = max(0, offset-limit)
		case len(fields) == 2 && (fields[0] == "d" || fields[0] == "e" || fields[0] == "m" || fields[0] == "a"):
			found := false
			for _, d := range page.Records[offset:min(offset+limit, len(page.Records))] {
				if fields[1] != d.Command.ID {
					continue
				}
				found = true
				var value any = d
				var err error
				if fields[0] != "d" {
					value, err = inspect(ctx, d, fields[0])
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					if _, err := fmt.Fprintln(out, "UNAVAILABLE: inspection failed or did not match the displayed decision. No inspection result retained; retry explicitly."); err != nil {
						return err
					}
				} else if err := emit(out, value); err != nil {
					return err
				}
				break
			}
			if !found {
				if _, err := fmt.Fprintln(out, "Choose an ID from the displayed page; no request sent."); err != nil {
					return err
				}
			}
		default:
			if _, err := fmt.Fprintln(out, "Unknown decision command; no request sent."); err != nil {
				return err
			}
		}
	}
}
