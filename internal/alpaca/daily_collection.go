package alpaca

import (
	"context"
	"encoding/json"
	"time"
)

// CollectDailyRead retains each successful live read by content and receipt
// time. Unlike historical checkpoints, empty or provisional pages are fetched
// again on the next cycle.
func (j *Journal) CollectDailyRead(ctx context.Context, c *Client, q Query) (Response, error) {
	res, err := c.Fetch(ctx, q)
	if err != nil {
		return Response{}, err
	}
	raw, err := json.Marshal(retainedPage{Query: q, Response: res})
	if err != nil {
		return Response{}, err
	}
	if err := j.writeOnce("daily-"+Digest(string(raw))+".json", raw); err != nil {
		return Response{}, err
	}
	return res, nil
}

// CollectDailyBars follows the same parser, paging and embargo as backfills,
// while retaining fresh responses rather than freezing a provisional result.
func (j *Journal) CollectDailyBars(ctx context.Context, c *Client, symbols []string, start, end time.Time) (DailyBars, error) {
	return c.readDailyBars(ctx, symbols, start, end, time.Now, func(ctx context.Context, q Query) (Response, error) {
		return j.CollectDailyRead(ctx, c, q)
	})
}

// DailyCalendar retains and validates one bounded exchange-calendar read.
func (j *Journal) DailyCalendar(ctx context.Context, c *Client, start, end string) ([]Session, error) {
	res, err := j.CollectDailyRead(ctx, c, Query{Capability: "market_calendar", Start: start, End: end})
	if err != nil {
		return nil, err
	}
	out := Result{}
	err = calendar(Recording{Start: start, End: end, Symbols: []string{"SPY"}, Original: res.Original, ReceivedAt: res.ReceivedAt}, &out)
	return out.Sessions, err
}
