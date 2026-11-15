// Command spycheck compares retained raw SPY bars through Mantik's adjustment
// engine with an independently acquired adjusted-close series. No network I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/marketdata"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func run() error {
	input := flag.String("input", "", "private JSON input with raw bars, actions, calendar and reference")
	tolerance := flag.String("tolerance-bps", "5", "relative adjusted-close tolerance")
	flag.Parse()
	if *input == "" || flag.NArg() != 0 {
		return fmt.Errorf("spycheck requires -input")
	}
	f, err := os.Open(*input)
	if err != nil {
		return err
	}
	defer f.Close()
	var in struct {
		ReferencePayDateReturn string                       `json:"reference_pay_date_return_multiple"`
		AssetID                string                       `json:"asset_id"`
		AsOf                   time.Time                    `json:"as_of"`
		Raw                    []storage.DailyBar           `json:"raw"`
		Actions                []marketdata.CorporateAction `json:"actions"`
		Calendar               []string                     `json:"calendar"`
		Reference              []marketdata.ReferenceClose  `json:"reference"`
	}
	d := json.NewDecoder(io.LimitReader(f, 32<<20))
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
