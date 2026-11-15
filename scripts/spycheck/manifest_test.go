package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func manifestFixture(t *testing.T) (string, []byte, []sessionClose, string) {
	t.Helper()
	dir := t.TempDir()
	in := closeBasisInput()
	c := in.SessionCloses[0]
	original := `{"symbol":"SPY","trades":[{"i":123,"p":248.11,"t":"2020-03-12T20:00:00.12Z","x":"P","z":"B","c":[" ","6"]}],"next_page_token":null}`
	c.SourceSHA256 = digest([]byte(original))
	page := map[string]any{"original": original, "sha256": c.SourceSHA256, "received_at": c.ObservedAt, "query": map[string]string{"start": "2020-03-12T20:00:00Z", "end": "2020-03-12T20:00:01Z", "feed": "sip"}}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.json")
	if err = os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"normalized":"input"}`)
	calendar := filepath.Join(dir, "calendar.json")
	calendarRaw := []byte(`[{"date":"2020-03-12"}]`)
	if err := os.WriteFile(calendar, calendarRaw, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"input_sha256": digest(input), "sources": []map[string]string{{"file": source, "sha256": digest(raw)}, {"file": calendar, "sha256": digest(calendarRaw)}}}
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path, input, []sessionClose{c}, source
}
func TestAuctionManifestBindsPricesToOriginalPrint(t *testing.T) {
	for _, name := range []string{"valid", "input_edit", "source_edit", "invented_digest", "invented_price", "invented_trade"} {
		t.Run(name, func(t *testing.T) {
			path, input, closes, source := manifestFixture(t)
			switch name {
			case "input_edit":
				input = append(input, ' ')
			case "source_edit":
				if err := os.WriteFile(source, []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "invented_digest":
				closes[0].SourceSHA256 = strings.Repeat("a", 64)
			case "invented_price":
				closes[0].Price = "255.24"
			case "invented_trade":
				closes[0].TradeID = "456"
			}
			err := verifyCloseManifest(path, input, closes)
			if (err == nil) != (name == "valid") {
				t.Fatal(name, err)
			}
		})
	}
}

func TestManifestGapRequiresBothNasdaqPriceAndEmptySIPOriginal(t *testing.T) {
	for _, variant := range []string{"valid", "currency", "grouped_currency", "invented_price", "missing_gap", "nonempty_gap"} {
		t.Run(variant, func(t *testing.T) {
			dir := t.TempDir()
			c := closeBasisInput().SessionCloses[0]
			c.SourceBasis = "nasdaq_sip_gap"
			c.SourceCondition = ""
			c.ExplanationURL = outageNotice
			c.Date = outageDate
			c.CloseAt = time.Date(2019, 8, 12, 20, 0, 0, 0, time.UTC)
			c.OccurredAt = time.Time{}
			c.TradeID = ""
			c.Price = "288.07"
			original := `{"symbol":"SPY","trades":null,"next_page_token":null}`
			if variant == "nonempty_gap" {
				original = `{"symbol":"SPY","trades":[{"p":288.07}],"next_page_token":null}`
			}
			c.GapSourceSHA256 = digest([]byte(original))
			gap, _ := json.Marshal(map[string]any{"original": original, "sha256": c.GapSourceSHA256, "received_at": c.ObservedAt, "query": map[string]string{"start": "2019-08-12T20:00:00Z", "end": "2019-08-12T20:00:01Z", "feed": "sip"}})
			nasdaq := []byte(`{"status":{"rCode":200},"data":{"symbol":"SPY","totalRecords":1,"tradesTable":{"rows":[{"date":"08/12/2019","close":"288.07"}]}}}`)
			if variant == "currency" {
				nasdaq = []byte(strings.ReplaceAll(string(nasdaq), `"288.07"`, `"$288.07"`))
			}
			if variant == "grouped_currency" {
				nasdaq = []byte(strings.ReplaceAll(string(nasdaq), `"288.07"`, `"$1,288.07"`))
				c.Price = "1288.07"
			}
			c.SourceSHA256 = digest(nasdaq)
			if variant == "invented_price" {
				c.Price = "288.10"
			}
			if variant == "missing_gap" {
				c.GapSourceSHA256 = strings.Repeat("b", 64)
			}
			input := []byte("input")
			sources := []map[string]string{}
			for name, raw := range map[string][]byte{"gap.json": gap, "nasdaq.json": nasdaq} {
				path := filepath.Join(dir, name)
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				sources = append(sources, map[string]string{"file": path, "sha256": digest(raw)})
			}
			raw, _ := json.Marshal(map[string]any{"input_sha256": digest(input), "sources": sources})
			path := filepath.Join(dir, "manifest.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			err := verifyCloseManifest(path, input, []sessionClose{c})
			if (err == nil) != (variant == "valid" || variant == "currency" || variant == "grouped_currency") {
				t.Fatal(variant, err)
			}
		})
	}
}
