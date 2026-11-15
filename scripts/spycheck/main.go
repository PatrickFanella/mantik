// Command spycheck compares retained raw SPY bars through Mantik's adjustment
// engine with an independently acquired adjusted-close series. No network I/O.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

type comparisonInput struct {
	PrimaryReference       []marketdata.ReferenceClose  `json:"primary_reference"`
	PrimaryReferenceReturn string                       `json:"primary_reference_pay_date_return"`
	ReferenceSampleEnd     string                       `json:"reference_sample_end"`
	ReferencePayDateReturn string                       `json:"reference_pay_date_return_multiple"`
	AssetID                string                       `json:"asset_id"`
	AsOf                   time.Time                    `json:"as_of"`
	Raw                    []storage.DailyBar           `json:"raw"`
	Actions                []marketdata.CorporateAction `json:"actions"`
	Calendar               []string                     `json:"calendar"`
	Reference              []marketdata.ReferenceClose  `json:"reference"`
	SessionCloses          []sessionClose               `json:"session_closes"`
}

func run() error {
	input := flag.String("input", "", "private JSON input with raw bars, actions, calendar and reference")
	basis := flag.String("close-basis", "raw-sip", "raw-sip or primary-close; auction mode requires complete source evidence")
	manifest := flag.String("manifest", "", "normalization manifest required for primary-close")
	tolerance := flag.String("tolerance-bps", "5", "relative adjusted-close tolerance")
	flag.Parse()
	if *input == "" || flag.NArg() != 0 || (*basis != "raw-sip" && *basis != "primary-close") {
		return fmt.Errorf("spycheck requires -input")
	}
	f, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	var in comparisonInput
	rawInput, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(rawInput) > 32<<20 {
		return fmt.Errorf("SPY input exceeds 32 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(rawInput))
	d.DisallowUnknownFields()
	if err = d.Decode(&in); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("expected one input")
	}
	series, err := marketdata.Adjust(in.AssetID, in.Raw, in.Actions, in.AsOf)
	if err != nil {
		return err
	}
	report, err := marketdata.CompareCloses(series, in.Reference, in.Calendar, *tolerance, in.ReferencePayDateReturn)
	if err != nil {
		return err
	}
	if *basis == "primary-close" {
		if *manifest == "" || len(in.SessionCloses) == 0 {
			return fmt.Errorf("primary-close requires manifest and complete auction evidence")
		}
		if err := verifyCloseManifest(*manifest, rawInput, in.SessionCloses); err != nil {
			return err
		}
		projected, err := sessionCloseSeries(in)
		if err != nil {
			return err
		}
		if len(in.PrimaryReference) != len(in.Calendar) || in.ReferenceSampleEnd == "" {
			return fmt.Errorf("primary-close requires the complete independent reference and its coverage boundary")
		}
		official, err := marketdata.CompareCloses(projected, in.PrimaryReference, in.Calendar, *tolerance, in.PrimaryReferenceReturn)
		if err != nil {
			return err
		}
		yahooDiagnostic, err := marketdata.CompareCloses(projected, in.Reference, in.Calendar, *tolerance, in.ReferencePayDateReturn)
		if err != nil {
			return err
		}
		combined := basisComparison{YahooPrimary: yahooDiagnostic, Passed: official.Passed, CloseBasis: "primary_exchange_close", ReferenceSampleEnd: in.ReferenceSampleEnd, Sessions: 0, RawSIP: report, SessionClose: official}
		for _, c := range in.SessionCloses {
			if c.SourceBasis == "primary_exchange_close" {
				combined.Sessions++
				if c.SourceCondition == "M" {
					combined.OfficialNotices++
				}
			} else {
				combined.ExplainedSessions = append(combined.ExplainedSessions, c)
			}
		}
		if len(combined.ExplainedSessions) > 0 {
			combined.CloseBasis = "primary_close_with_explained_trade_gap"
		}
		for i, b := range in.Raw {
			if !samePrice(b.Close, in.SessionCloses[i].Price) {
				combined.Reconciliations = append(combined.Reconciliations, closeReconciliation{Date: b.Date, RawClose: b.Close, SessionClose: in.SessionCloses[i].Price, SourceSHA256: in.SessionCloses[i].SourceSHA256})
			}
		}
		if err := json.NewEncoder(os.Stdout).Encode(combined); err != nil {
			return err
		}
		if !combined.Passed {
			return fmt.Errorf("SPY session-close comparison failed")
		}
		return nil
	}
	if len(in.SessionCloses) > 0 {
		return fmt.Errorf("session close evidence requires explicit -close-basis primary-close")
	}
	if err = json.NewEncoder(os.Stdout).Encode(report); err != nil {
		return err
	}
	if !report.Passed {
		return fmt.Errorf("SPY comparison failed; inspect reported gaps/differences")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
