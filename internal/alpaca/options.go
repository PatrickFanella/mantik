package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
)

var standardOption = regexp.MustCompile(`^[A-Z]{1,6}[0-9]{6}[CP][0-9]{8}$`)

// OptionSpec bounds a resumable acquisition. End is exclusive; expiry bounds
// are inclusive. Strike bounds are optional exact dollar strings.
type OptionSpec struct {
	Underlyings            []string
	Start, End             time.Time
	ExpiryStart, ExpiryEnd string
	MinStrike, MaxStrike   string
}

// OptionContract is the supported subset of the source listing. Full source
// objects are kept in the store and complete responses in the private journal.
type OptionContract struct {
	ID         string `json:"id"`
	Symbol     string `json:"symbol"`
	Underlying string `json:"underlying_symbol"`
	Expiry     string `json:"expiration_date"`
	Strike     string `json:"strike_price"`
	Type       string `json:"type"`
}

// OptionWriter accepts an atomic page of point-in-time observations.
type OptionWriter interface {
	RecordOptionData(context.Context, []alpacarecord.OptionRecord) (alpacarecord.OptionReceipt, error)
}

// OptionCounts includes skipped nonstandard contracts, which are retained as
// references but cannot be sent to the provider's standard-symbol bar API.
type OptionCounts struct {
	Contracts int `json:"contracts"`
	Bars      int `json:"bars"`
	Trades    int `json:"trades"`
	Skipped   int `json:"skipped_nonstandard"`
	Inserted  int `json:"inserted"`
	Unchanged int `json:"unchanged"`
}

// Validate checks a complete job before acquisition or store access.
func (s OptionSpec) Validate() error {
	if _, err := barSymbols(s.Underlyings); err != nil {
		return err
	}
	if len(s.Underlyings) > 10 || s.Start.Before(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) || !s.End.After(s.Start) || s.End.After(time.Now().UTC().Add(-RecentSIPDelay)) {
		return errors.New("options require 1–10 underlyings, start since February 2024, fixed end at least 15 minutes old")
	}
	from, e1 := time.Parse(time.DateOnly, s.ExpiryStart)
	to, e2 := time.Parse(time.DateOnly, s.ExpiryEnd)
	if e1 != nil || e2 != nil || to.Before(from) {
		return errors.New("invalid option expiration range")
	}
	for _, v := range []string{s.MinStrike, s.MaxStrike} {
		if v != "" && (!decimalPattern.MatchString(v) || strings.Trim(v, "0.") == "") {
			return errors.New("invalid option strike bound")
		}
	}
	if s.MinStrike != "" && s.MaxStrike != "" {
		a, _ := new(big.Rat).SetString(s.MinStrike)
		b, _ := new(big.Rat).SetString(s.MaxStrike)
		if a.Cmp(b) > 0 {
			return errors.New("reversed strike bounds")
		}
	}
	return nil
}

func (c *Client) optionsTarget(q Query, now time.Time) (string, error) {
	if q.PageToken != "" && !alpacarecord.ValidPageToken(q.PageToken) {
		return "", errors.New("invalid option page token")
	}
	v := url.Values{"limit": {"1000"}}
	if q.PageToken != "" {
		v.Set("page_token", q.PageToken)
	}
	if q.Capability == "option_contracts" {
		s := OptionSpec{Underlyings: q.Symbols, Start: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 2, 2, 0, 0, 0, 0, time.UTC), ExpiryStart: q.Start, ExpiryEnd: q.End, MinStrike: q.MinStrike, MaxStrike: q.MaxStrike}
		if err := s.Validate(); err != nil {
			return "", err
		}
		if !slices.Contains([]string{"active", "inactive"}, q.Status) {
			return "", errors.New("option listings require active or inactive status")
		}
		v.Set("underlying_symbols", strings.Join(q.Symbols, ","))
		v.Set("status", q.Status)
		v.Set("expiration_date_gte", q.Start)
		v.Set("expiration_date_lte", q.End)
		v.Set("limit", "100")
		if q.MinStrike != "" {
			v.Set("strike_price_gte", q.MinStrike)
		}
		if q.MaxStrike != "" {
			v.Set("strike_price_lte", q.MaxStrike)
		}
		return c.calendarOrigin + "/v2/options/contracts?" + v.Encode(), nil
	}
	start, e1 := time.Parse(time.RFC3339, q.Start)
	end, e2 := time.Parse(time.RFC3339, q.End)
	if e1 != nil || e2 != nil || start.Before(time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)) || end.Before(start) || end.After(now.Add(-RecentSIPDelay)) || len(q.Symbols) != 1 || !standardOption.MatchString(q.Symbols[0]) {
		return "", errors.New("invalid indicative option history request")
	}
	v.Set("symbols", q.Symbols[0])
	v.Set("start", q.Start)
	v.Set("end", q.End)
	v.Set("sort", "asc")
	// Historical endpoints reject a feed selector. Classify their default
	// conservatively and retain the unverified selection in each record.
	if q.Capability == "option_bars" {
		v.Set("timeframe", "1Day")
		return c.dataOrigin + "/v1beta1/options/bars?" + v.Encode(), nil
	}
	if q.Capability == "option_trades" {
		return c.dataOrigin + "/v1beta1/options/trades?" + v.Encode(), nil
	}
	return "", errors.New("unsupported option capability")
}

// optionPage requires an explicit collection and token envelope. Missing keys
// cannot silently turn an authentication/error object into an empty collection.
func optionPage(raw, key string) (json.RawMessage, string, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &object) != nil || object == nil || object[key] == nil || object["next_page_token"] == nil {
		return nil, "", errors.New("invalid option page envelope")
	}
	var next *string
	if json.Unmarshal(object["next_page_token"], &next) != nil || next != nil && (*next == "" || !alpacarecord.ValidPageToken(*next)) {
		return nil, "", errors.New("invalid option next page token")
	}
	if next == nil {
		return object[key], "", nil
	}
	return object[key], *next, nil
}

func pages(ctx context.Context, j *Journal, c *Client, q Query, consume func(Response) (string, error)) error {
	seen := map[string]bool{}
	for n := 0; n < MaxBarPages; n++ {
		res, err := j.RetainedRead(ctx, c, q)
		if err != nil {
			return err
		}
		next, err := consume(res)
		if err != nil {
			return err
		}
		if next == "" {
			return nil
		}
		if seen[next] {
			return errors.New("repeated option page token")
		}
		seen[next] = true
		q.PageToken = next
	}
	return errors.New("option pagination exceeded bound")
}

// CollectOptions retains and replays complete listing and data pages. It reads
// both active and inactive contracts; interruptions resume from cached pages.
// All availability clocks are actual page receipts, never backdated to a trade.
func (j *Journal) CollectOptions(ctx context.Context, c *Client, store OptionWriter, s OptionSpec, progress io.Writer) (OptionCounts, error) {
	out := OptionCounts{}
	if err := s.Validate(); err != nil {
		return out, err
	}
	write := func(records []alpacarecord.OptionRecord) error {
		if len(records) == 0 {
			return nil
		}
		r, err := store.RecordOptionData(ctx, records)
		out.Inserted += r.Inserted
		out.Unchanged += r.Unchanged
		return err
	}
	for _, underlying := range s.Underlyings {
		contracts := map[string]OptionContract{}
		for _, status := range []string{"active", "inactive"} {
			q := Query{Capability: "option_contracts", Symbols: []string{underlying}, Start: s.ExpiryStart, End: s.ExpiryEnd, Status: status, MinStrike: s.MinStrike, MaxStrike: s.MaxStrike}
			err := pages(ctx, j, c, q, func(res Response) (string, error) {
				raw, next, err := optionPage(res.Original, "option_contracts")
				if err != nil {
					return "", err
				}
				var entries []json.RawMessage
				if json.Unmarshal(raw, &entries) != nil || entries == nil {
					return "", errors.New("invalid option contract list")
				}
				records := []alpacarecord.OptionRecord{}
				for _, entry := range entries {
					var contract OptionContract
					if json.Unmarshal(entry, &contract) != nil || contract.ID == "" || contract.Symbol == "" || contract.Underlying != underlying || contract.Expiry < s.ExpiryStart || contract.Expiry > s.ExpiryEnd || !slices.Contains([]string{"call", "put"}, contract.Type) || !decimalPattern.MatchString(contract.Strike) || strings.Trim(contract.Strike, "0.") == "" {
						return "", errors.New("invalid option contract metadata")
					}
					if _, err := time.Parse(time.DateOnly, contract.Expiry); err != nil {
						return "", err
					}
					if prior, ok := contracts[contract.Symbol]; ok {
						if prior != contract {
							return "", errors.New("conflicting option contract identity")
						}
						continue
					}
					contracts[contract.Symbol] = contract
					out.Contracts++
					records = append(records, alpacarecord.OptionRecord{Kind: "contract", Symbol: contract.Symbol, Underlying: underlying, Key: contract.ID, Feed: "reference", OccurredAt: res.ReceivedAt, ObservedAt: res.ReceivedAt, AvailableAt: res.ReceivedAt, Data: entry})
				}
				return next, write(records)
			})
			if err != nil {
				return out, err
			}
		}
		symbols := make([]string, 0, len(contracts))
		for symbol := range contracts {
			symbols = append(symbols, symbol)
		}
		slices.Sort(symbols)
		for _, symbol := range symbols {
			if !standardOption.MatchString(symbol) {
				out.Skipped++
				continue
			}
			for _, kind := range []string{"bar", "trade"} {
				capability, envelope := "option_bars", "bars"
				if kind == "trade" {
					capability, envelope = "option_trades", "trades"
				}
				q := Query{Capability: capability, Symbols: []string{symbol}, Start: s.Start.UTC().Format(time.RFC3339Nano), End: s.End.UTC().Add(-time.Nanosecond).Format(time.RFC3339Nano)}
				var previous time.Time
				occurrences := map[string]int{}
				err := pages(ctx, j, c, q, func(res Response) (string, error) {
					raw, next, err := optionPage(res.Original, envelope)
					if err != nil {
						return "", err
					}
					var bySymbol map[string][]json.RawMessage
					if json.Unmarshal(raw, &bySymbol) != nil || bySymbol == nil {
						return "", errors.New("invalid option data map")
					}
					records := []alpacarecord.OptionRecord{}
					for name, entries := range bySymbol {
						if name != symbol {
							return "", errors.New("unrequested option symbol")
						}
						for _, entry := range entries {
							var at time.Time
							var key string
							if kind == "bar" {
								var raw rawBar
								if err := json.Unmarshal(entry, &raw); err != nil {
									return "", err
								}
								b, err := raw.parse()
								if err != nil {
									return "", err
								}
								at = b.Time
								key = at.Format(time.DateOnly)
								if !previous.IsZero() && !at.After(previous) {
									return "", errors.New("option bars repeated or out of order")
								}
							} else {
								var trade struct {
									T time.Time   `json:"t"`
									P json.Number `json:"p"`
									S json.Number `json:"s"`
									I json.Number `json:"i"`
								}
								if err := json.Unmarshal(entry, &trade); err != nil {
									return "", err
								}
								size, err := strconv.ParseInt(trade.S.String(), 10, 64)
								if err != nil || size <= 0 || !decimalPattern.MatchString(trade.P.String()) || strings.Trim(trade.P.String(), "0.") == "" {
									return "", errors.New("invalid indicative trade")
								}
								at = trade.T
								if trade.I.String() != "" {
									if _, err := strconv.ParseUint(trade.I.String(), 10, 64); err != nil {
										return "", errors.New("invalid option trade ID")
									}
									key = at.UTC().Format(time.RFC3339Nano) + "-" + trade.I.String()
								} else {
									// Indicative history omits IDs. Keep identical events
									// distinct using their occurrence in the retained stream.
									var fields map[string]json.RawMessage
									if err := json.Unmarshal(entry, &fields); err != nil {
										return "", err
									}
									canonical, err := json.Marshal(fields)
									if err != nil {
										return "", err
									}
									identity := at.UTC().Format(time.RFC3339Nano) + "-derived-" + Digest(string(canonical))[:32]
									occurrences[identity]++
									key = identity + "-" + strconv.Itoa(occurrences[identity])
								}
								if at.Before(previous) {
									return "", errors.New("option trades out of order")
								}
							}
							if at.Before(s.Start) || !at.Before(s.End) || at.After(res.ReceivedAt) {
								return "", errors.New("option data outside requested range")
							}
							previous = at
							records = append(records, alpacarecord.OptionRecord{Kind: kind, Symbol: symbol, Underlying: underlying, Key: key, Feed: "indicative", FeedSelection: "provider_default_unverified", OccurredAt: at, ObservedAt: res.ReceivedAt, AvailableAt: res.ReceivedAt, Data: entry})
							if kind == "bar" {
								out.Bars++
							} else {
								out.Trades++
							}
						}
					}
					return next, write(records)
				})
				if err != nil {
					return out, err
				}
			}
			if _, err := fmt.Fprintf(progress, "underlying=%s contract=%s bars=%d trades=%d inserted=%d unchanged=%d\n", underlying, symbol, out.Bars, out.Trades, out.Inserted, out.Unchanged); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}
