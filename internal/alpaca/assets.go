package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StockAsset is a source asset identity. Current status and tradability do not
// declare historical listing/delisting dates or past order eligibility.
type StockAsset struct {
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Class    string `json:"class"`
	Exchange string `json:"exchange"`
	Status   string `json:"status"`
	Tradable bool   `json:"tradable"`
}

// AssetSnapshot retains both active and inactive reference observations. The
// provider's inactive list is incomplete; historical stock screens may be biased.
type AssetSnapshot struct {
	ObservedAt time.Time    `json:"observed_at"`
	Assets     []StockAsset `json:"assets"`
}

// StockAssets fetches both US-equity lists with the bounded GET-only client.
// Asset lists have no paging and exceed the ordinary 1 MiB data-page bound;
// each is capped at 16 MiB and retained in <=1 MiB chunks plus a digest manifest.
func (j *Journal) StockAssets(ctx context.Context, c *Client) (AssetSnapshot, error) {
	out := AssetSnapshot{Assets: []StockAsset{}}
	seen := map[string]bool{}
	for _, status := range []string{"active", "inactive"} {
		res, err := c.Fetch(ctx, Query{Capability: "stock_assets", Status: status})
		if err != nil {
			return out, err
		}
		chunks := []string{}
		for start := 0; start < len(res.Original); start += MaxBytes {
			end := min(start+MaxBytes, len(res.Original))
			raw := []byte(res.Original[start:end])
			digest := Digest(string(raw))
			name := "asset-bytes-" + digest + ".raw"
			if err := j.writeOnce(name, raw); err != nil {
				return out, err
			}
			chunks = append(chunks, digest)
		}
		manifest, err := json.Marshal(struct {
			Status string
			At     time.Time
			SHA256 string
			Chunks []string
		}{status, res.ReceivedAt, res.SHA256, chunks})
		if err != nil {
			return out, err
		}
		if err := j.writeOnce("asset-list-"+Digest(string(manifest))+".json", manifest); err != nil {
			return out, err
		}
		var assets []StockAsset
		if err := json.Unmarshal([]byte(res.Original), &assets); err != nil || assets == nil {
			return out, errors.New("invalid asset list response")
		}
		for _, a := range assets {
			if a.ID == "" || a.Symbol == "" || a.Class != "us_equity" || a.Status != status || seen[a.ID] {
				return out, fmt.Errorf("invalid or conflicting %s asset reference", status)
			}
			seen[a.ID] = true
			out.Assets = append(out.Assets, a)
		}
		if res.ReceivedAt.After(out.ObservedAt) {
			out.ObservedAt = res.ReceivedAt
		}
	}
	return out, nil
}
