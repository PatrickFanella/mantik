package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"
)

type retainedPage struct {
	Query    Query    `json:"query"`
	Response Response `json:"response"`
}

// RetainedRead checkpoints an immutable historical page before its caller uses
// it. A journal's process lock serializes collectors. On restart, successful
// pages are revalidated and replayed without a provider request. Use a dedicated
// journal for one historical acquisition, not for refreshes of current data.
func (j *Journal) RetainedRead(ctx context.Context, c *Client, q Query) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	if q.Capability != "daily_bars" && q.Capability != "option_contracts" && q.Capability != "option_bars" && q.Capability != "option_trades" {
		return Response{}, errors.New("unsupported historical read")
	}
	if _, err := c.target(q, time.Now().UTC()); err != nil {
		return Response{}, err
	}
	key, err := json.Marshal(q)
	if err != nil {
		return Response{}, err
	}
	name := "history-" + Digest(string(key)) + ".json"
	raw, err := j.read(name, maxJournalFile)
	if err == nil {
		var page retainedPage
		if json.Unmarshal(raw, &page) != nil {
			return Response{}, ErrArchiveIntegrity
		}
		bound, err := json.Marshal(page.Query)
		res := page.Response
		if err != nil || string(bound) != string(key) || res.ReceivedAt.IsZero() || res.SHA256 != Digest(res.Original) || len(res.Original) > MaxBytes {
			return Response{}, ErrArchiveIntegrity
		}
		return res, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Response{}, err
	}
	res, err := c.Fetch(ctx, q)
	if err != nil {
		return Response{}, err
	}
	raw, err = json.Marshal(retainedPage{Query: q, Response: res})
	if err != nil {
		return Response{}, err
	}
	if err := j.writeOnce(name, raw); err != nil {
		return Response{}, err
	}
	return res, nil
}

// BackfillDailyBars follows the ordinary bar parser, embargo and pagination
// contract, retaining each page so even an interrupted symbol can resume.
func (j *Journal) BackfillDailyBars(ctx context.Context, c *Client, symbol string, start, end time.Time) (DailyBars, error) {
	return c.readDailyBars(ctx, []string{symbol}, start, end, time.Now, func(ctx context.Context, q Query) (Response, error) {
		return j.RetainedRead(ctx, c, q)
	})
}
