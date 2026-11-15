package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func boundedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("SPY evidence file exceeds bound")
	}
	return raw, nil
}

type closePage struct {
	Original   string                            `json:"original"`
	SHA256     string                            `json:"sha256"`
	ReceivedAt time.Time                         `json:"received_at"`
	Query      struct{ Start, End, Feed string } `json:"query"`
}
type closeBody struct {
	Symbol string `json:"symbol"`
	Trades []struct {
		ID         json.Number `json:"i"`
		Price      json.Number `json:"p"`
		At         time.Time   `json:"t"`
		Exchange   string      `json:"x"`
		Tape       string      `json:"z"`
		Conditions []string    `json:"c"`
	} `json:"trades"`
}

func pageClock(page closePage, c sessionClose) error {
	start, e1 := time.Parse(time.RFC3339Nano, page.Query.Start)
	end, e2 := time.Parse(time.RFC3339Nano, page.Query.End)
	if e1 != nil || e2 != nil || page.Query.Feed != "sip" || !start.Equal(c.CloseAt) || !end.Equal(c.CloseAt.Add(time.Second)) || page.ReceivedAt.IsZero() || c.ObservedAt.Before(page.ReceivedAt) {
		return errors.New("close evidence query or clock mismatch")
	}
	return nil
}
func verifyNasdaq(raw []byte, c sessionClose) error {
	var source struct {
		Status struct {
			Code int `json:"rCode"`
		} `json:"status"`
		Data struct {
			Symbol string `json:"symbol"`
			Total  int    `json:"totalRecords"`
			Table  struct {
				Rows []struct{ Date, Close string } `json:"rows"`
			} `json:"tradesTable"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &source) != nil || source.Status.Code != 200 || source.Data.Symbol != "SPY" || source.Data.Total != len(source.Data.Table.Rows) || c.SourceBasis != "nasdaq_sip_gap" || c.Date != outageDate || c.ExplanationURL != outageNotice {
		return errors.New("invalid Nasdaq gap reference")
	}
	count := 0
	for _, row := range source.Data.Table.Rows {
		if row.Date == "08/12/2019" {
			count++
			close := strings.TrimSpace(strings.TrimPrefix(strings.ReplaceAll(row.Close, ",", ""), "$"))
			if !samePrice(close, c.Price) {
				return errors.New("gap price differs from original Nasdaq close")
			}
		}
	}
	if count != 1 {
		return errors.New("Nasdaq gap date missing or duplicate")
	}
	return nil
}

// verifyCloseManifest binds the normalized input and every source file. Selected
// prices must match original SIP messages, or the independent Nasdaq fallback
// together with an empty SIP response for the single documented outage.
func verifyCloseManifest(path string, input []byte, closes []sessionClose) error {
	raw, err := boundedFile(path, 4<<20)
	if err != nil {
		return err
	}
	var manifest struct {
		InputSHA256 string `json:"input_sha256"`
		Sources     []struct {
			File   string `json:"file"`
			SHA256 string `json:"sha256"`
		} `json:"sources"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.InputSHA256 != digest(input) || len(manifest.Sources) == 0 || len(manifest.Sources) > 100000 {
		return errors.New("SPY input does not match normalization manifest")
	}
	wanted, gaps := map[string][]sessionClose{}, map[string][]sessionClose{}
	for _, c := range closes {
		wanted[c.SourceSHA256] = append(wanted[c.SourceSHA256], c)
		if c.GapSourceSHA256 != "" {
			gaps[c.GapSourceSHA256] = append(gaps[c.GapSourceSHA256], c)
		}
	}
	found, foundGaps := map[string]bool{}, map[string]bool{}
	for _, source := range manifest.Sources {
		if !filepath.IsAbs(source.File) {
			return errors.New("SPY manifest requires absolute source paths")
		}
		raw, err := boundedFile(source.File, 16<<20)
		if err != nil {
			return err
		}
		if digest(raw) != source.SHA256 {
			return errors.New("SPY source does not match normalization manifest")
		}
		if len(wanted[source.SHA256]) > 0 {
			for _, c := range wanted[source.SHA256] {
				if err := verifyNasdaq(raw, c); err != nil {
					return err
				}
			}
			found[source.SHA256] = true
			continue
		}
		var page closePage
		if json.Unmarshal(raw, &page) != nil {
			continue
		} // Calendar arrays and the LEAN ZIP are independently hashed source formats.
		if len(wanted[page.SHA256])+len(gaps[page.SHA256]) == 0 {
			continue
		}
		if digest([]byte(page.Original)) != page.SHA256 {
			return errors.New("close original digest mismatch")
		}
		var body closeBody
		if json.Unmarshal([]byte(page.Original), &body) != nil || body.Symbol != "SPY" {
			return errors.New("invalid SPY close source")
		}
		for _, c := range wanted[page.SHA256] {
			if err := pageClock(page, c); err != nil {
				return err
			}
			matched := false
			for _, trade := range body.Trades {
				extra := slices.Clone(trade.Conditions)
				extra = slices.DeleteFunc(extra, func(v string) bool { return v == " " || v == "6" || v == "M" })
				if trade.ID.String() == c.TradeID && trade.At.Equal(c.OccurredAt) && trade.Exchange == "P" && trade.Tape == "B" && slices.Contains(trade.Conditions, c.SourceCondition) && len(extra) == 0 && samePrice(trade.Price.String(), c.Price) {
					matched = true
					break
				}
			}
			if !matched {
				return errors.New("declared close does not match original SIP print")
			}
			found[page.SHA256] = true
		}
		for _, c := range gaps[page.SHA256] {
			if err := pageClock(page, c); err != nil {
				return err
			}
			var envelope map[string]json.RawMessage
			if json.Unmarshal([]byte(page.Original), &envelope) != nil || envelope["trades"] == nil || envelope["next_page_token"] == nil || c.Date != outageDate || len(body.Trades) != 0 {
				return errors.New("unsupported explained SIP trade gap")
			}
			foundGaps[page.SHA256] = true
		}
	}
	for h := range wanted {
		if !found[h] {
			return errors.New("close source missing from normalization manifest")
		}
	}
	for h := range gaps {
		if !foundGaps[h] {
			return errors.New("empty SIP source missing from normalization manifest")
		}
	}
	return nil
}
