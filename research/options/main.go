// Command options runs offline retrospective income-option research. It has no
// database, subscription or broker client, and cannot publish trial evidence.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/engine"
	"git.subcult.tv/subculture-collective/mantik/internal/strategy/library"
)

const policy = "retrospective-final-snapshot-close-plus15m-v1"

type report struct {
	Policy, EvidenceClass, InputSHA256 string
	QualificationCredit                bool
	Sources                            []page
	OriginalRecords                    []originalRecord
	Assumptions                        []string
	Coverage                           coverage
	Backtest                           engine.OptionBacktest
}

type coverage struct {
	CalendarDates      []string
	MissingUnderlying  []string
	MissingOptionDates map[string][]string
}

func archiveCoverage(a *archive) coverage {
	c := coverage{MissingOptionDates: map[string][]string{}}
	for date := range a.closes {
		c.CalendarDates = append(c.CalendarDates, date)
	}
	slices.Sort(c.CalendarDates)
	for _, date := range c.CalendarDates {
		if a.spots[date] == 0 {
			c.MissingUnderlying = append(c.MissingUnderlying, date)
		}
		for symbol, bars := range a.bars {
			if _, ok := bars[date]; !ok {
				c.MissingOptionDates[symbol] = append(c.MissingOptionDates[symbol], date)
			}
		}
	}
	return c
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("options-research", flag.ContinueOnError)
	fs.SetOutput(errOut)
	path := fs.String("input", "", "private archived-page manifest JSON")
	pin := fs.String("input-sha256", "", "SHA-256 of exact manifest bytes")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || *path == "" || len(*pin) != 64 {
		return errors.New("input and input-sha256 are required")
	}
	f, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	if err != nil || len(raw) > 32<<20 {
		return errors.New("research manifest exceeds 32 MiB or is unreadable")
	}
	if digest(raw) != *pin {
		return errors.New("research manifest hash differs")
	}
	var input manifest
	if err := json.Unmarshal(raw, &input); err != nil {
		return err
	}
	source, err := readArchive(input)
	if err != nil {
		return err
	}
	recipe, err := library.NewOption(input.Recipe)
	if err != nil {
		return err
	}
	stocks := map[string]int{}
	if input.PaidShares > 0 {
		stocks[input.AssetID] = input.PaidShares
	}
	result, runErr := engine.RunOptionBacktest(ctx, engine.OptionBacktestConfig{
		Recipe: recipe, AssetID: input.AssetID, Times: input.Times,
		Book: engine.OptionConfig{Source: source, Account: account.Cash,
			SettledCash: input.SettledCash, Stocks: stocks,
			FeePerContract: input.FeePerContract, SlippageBps: input.SlippageBps},
	})
	result.EvidenceStatus = "retrospective_indicative_unqualified"
	// Retain source hashes, request URLs and actual receipts in every report.
	// Exact original bodies remain in the pinned input, never projected in place.
	sources := append([]page(nil), input.Pages...)
	for i := range sources {
		sources[i].Body = ""
	}
	result.Provenance.Evidence = "retrospective_indicative_unqualified"
	encodeErr := json.NewEncoder(out).Encode(report{Policy: policy,
		EvidenceClass: "retrospective_indicative_unqualified", InputSHA256: *pin,
		Sources: sources, OriginalRecords: source.originals, Coverage: archiveCoverage(source), Backtest: result,
		Assumptions: []string{
			"Archived final snapshots may contain later corrections; original acquisition clocks remain in Sources and the pinned input.",
			"Scenario daily bars become known 15 minutes after retained calendar close; reference metadata is assumed known with the first retained option bar.",
			"The supplied contracts and decision sessions are a selected research sample, not complete historical chain membership or continuous coverage.",
			"Indicative OHLC and European BSM estimates are not executable OPRA quotes, assignment simulation or broker fills.",
			"Declared cash and fully paid shares are simulation capital, independent of the owner capped paper book; no admission or qualification credit.",
		}})
	return errors.Join(runErr, encodeErr)
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
