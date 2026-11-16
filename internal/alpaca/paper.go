package alpaca

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"time"
)

// ErrSubmissionUnknown requires lookup by the original client ID, never a new POST.
// ErrPaperNotSubmitted means no durable adapter intent exists for this ID.
var ErrPaperNotSubmitted = errors.New("paper order was not submitted")

var ErrSubmissionUnknown = errors.New("paper submission unresolved; recover with the same client order ID")

// PaperCredentials selects either API keys or an OAuth bearer, never both.
type PaperCredentials struct {
	Key        string `json:"key,omitempty"`
	Secret     string `json:"secret,omitempty"`
	OAuthToken string `json:"oauth_token,omitempty"`
}

// StockOrder is deliberately limited to simple day stock orders.
type StockOrder struct {
	ClientOrderID string `json:"client_order_id"`
	Symbol        string `json:"symbol"`
	Qty           string `json:"qty"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	TimeInForce   string `json:"time_in_force"`
	LimitPrice    string `json:"limit_price,omitempty"`
}

// PaperOrder retains exact decimal fill quantities and venue state.
type PaperOrder struct {
	ID             string  `json:"id"`
	ClientOrderID  string  `json:"client_order_id"`
	Symbol         string  `json:"symbol"`
	Qty            string  `json:"qty"`
	Side           string  `json:"side"`
	Type           string  `json:"type"`
	TimeInForce    string  `json:"time_in_force"`
	LimitPrice     string  `json:"limit_price"`
	FilledQty      string  `json:"filled_qty"`
	FilledAvgPrice *string `json:"filled_avg_price"`
	Status         string  `json:"status"`
}

// PaperTrader uses a private, process-locked journal to fence submission retries.
// Its endpoint is fixed to Alpaca paper; the market-read Client remains read-only.
type PaperTrader struct {
	credentials PaperCredentials
	account     string
	journal     *Journal
	http        *http.Client
	origin      string
	gate        chan struct{}
}

// PaperAccountID reports the explicitly bound expected paper account identity.
func (p *PaperTrader) PaperAccountID() string { return p.account }

// Order recovers an already-journaled intent by its original client ID. It
// never POSTs, including when a capped reservation predates adapter submission.
func (p *PaperTrader) Order(ctx context.Context, clientID string) (PaperOrder, error) {
	if !paperID.MatchString(clientID) {
		return PaperOrder{}, errors.New("invalid client order ID")
	}
	if err := p.enter(); err != nil {
		return PaperOrder{}, err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return PaperOrder{}, err
	}
	if _, err := p.journal.read("paper-abort-"+clientID+".json", MaxBytes); err == nil {
		return PaperOrder{}, ErrPaperNotSubmitted
	} else if !errors.Is(err, os.ErrNotExist) {
		return PaperOrder{}, err
	}
	raw, err := p.journal.read("paper-intent-"+clientID+".json", MaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return PaperOrder{}, ErrPaperNotSubmitted
	}
	if err != nil {
		return PaperOrder{}, err
	}
	var intent StockOrder
	if json.Unmarshal(raw, &intent) != nil || intent.ClientOrderID != clientID || intent.validate() != nil {
		return PaperOrder{}, errors.New("invalid retained paper intent")
	}
	return p.lookup(ctx, intent)
}

// NewPaperTrader requires an explicitly expected paper account ID and durable journal.
func NewPaperTrader(c PaperCredentials, account string, j *Journal) (*PaperTrader, error) {
	keys := c.Key != "" && c.Secret != "" && c.OAuthToken == ""
	bearer := c.Key == "" && c.Secret == "" && c.OAuthToken != ""
	if (!keys && !bearer) || !paperID.MatchString(account) || j == nil {
		return nil, errors.New("paper trader requires one credential mode, account ID and journal")
	}
	return &PaperTrader{credentials: c, account: account, journal: j, origin: "https://paper-api.alpaca.markets", gate: make(chan struct{}, 1), http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

var paperID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var paperDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,15})(\.[0-9]{1,9})?$`)

func positivePaperDecimal(s string) bool {
	if !paperDecimal.MatchString(s) {
		return false
	}
	r, ok := new(big.Rat).SetString(s)
	return ok && r.Sign() > 0
}

// Validate checks the adapter order grammar before a local reservation is made.
func (o StockOrder) Validate() error { return o.validate() }

func (o StockOrder) validate() error {
	if !paperID.MatchString(o.ClientOrderID) || !symbolPattern.MatchString(o.Symbol) || !positivePaperDecimal(o.Qty) || !slices.Contains([]string{"buy", "sell"}, o.Side) || o.TimeInForce != "day" || !slices.Contains([]string{"market", "limit"}, o.Type) || (o.Type == "limit" && !positivePaperDecimal(o.LimitPrice)) || (o.Type == "market" && o.LimitPrice != "") {
		return errors.New("invalid simple day stock order")
	}
	return nil
}

// request retains every bounded raw response before its caller decodes it.
// A retention failure stops the operation even if the venue accepted the order.
func (p *PaperTrader) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.origin+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("invalid paper request")
	}
	if p.credentials.OAuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.credentials.OAuthToken)
	} else {
		req.Header.Set("APCA-API-KEY-ID", p.credentials.Key)
		req.Header.Set("APCA-API-SECRET-KEY", p.credentials.Secret)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return nil, 0, errors.New("paper transport failed")
	}
	raw, readErr := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	_ = res.Body.Close()
	partial := readErr != nil || len(raw) > MaxBytes
	if len(raw) > MaxBytes {
		raw = raw[:MaxBytes]
	}
	record, err := json.Marshal(struct {
		Method, Path string
		Status       int
		At           time.Time
		Partial      bool
		SHA256       string
		Raw          []byte
	}{method, path, res.StatusCode, p.journal.stamp(), partial, Digest(string(raw)), raw})
	if err != nil {
		return nil, res.StatusCode, err
	}
	if err = p.journal.writeOnce("paper-response-"+rand.Text()+".json", record); err != nil {
		return nil, res.StatusCode, err
	}
	if partial {
		return nil, res.StatusCode, errors.New("paper response incomplete or oversized")
	}
	return raw, res.StatusCode, nil
}

func (p *PaperTrader) bind(ctx context.Context) error {
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/account", nil)
	if err != nil {
		return err
	}
	var a struct {
		ID             string `json:"id"`
		Status         string `json:"status"`
		TradingBlocked bool   `json:"trading_blocked"`
		AccountBlocked bool   `json:"account_blocked"`
	}
	if status != 200 || json.Unmarshal(raw, &a) != nil || a.ID != p.account || a.Status != "ACTIVE" || a.TradingBlocked || a.AccountBlocked {
		return errors.New("paper account mismatch, unavailable or blocked")
	}
	return p.journal.writeOnce("paper-account.json", []byte(p.account))
}

func (p *PaperTrader) enter() error {
	select {
	case p.gate <- struct{}{}:
		return nil
	default:
		return ErrBackpressure
	}
}

// Submit commits an intent before the sole POST. Any later call for that ID
// performs only GET, including after process death or a 404 on recovery.
func (p *PaperTrader) Submit(ctx context.Context, o StockOrder) (PaperOrder, error) {
	return p.SubmitChecked(ctx, o, nil)
}

// SubmitChecked checks initial submission authority after preparatory reads and
// durable intent retention, immediately before POST. A refusal durably aborts
// that ID. Retained intents recover by GET only, without calling beforePost.
func (p *PaperTrader) SubmitChecked(ctx context.Context, o StockOrder, beforePost func() error) (PaperOrder, error) {
	if err := o.validate(); err != nil {
		return PaperOrder{}, err
	}
	if err := p.enter(); err != nil {
		return PaperOrder{}, err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return PaperOrder{}, err
	}
	if _, err := p.journal.read("paper-abort-"+o.ClientOrderID+".json", MaxBytes); err == nil {
		return PaperOrder{}, ErrPaperNotSubmitted
	} else if !errors.Is(err, os.ErrNotExist) {
		return PaperOrder{}, err
	}
	intent, _ := json.Marshal(o)
	name := "paper-intent-" + o.ClientOrderID + ".json"
	previous, err := p.journal.read(name, MaxBytes)
	if err == nil {
		if !bytes.Equal(previous, intent) {
			return PaperOrder{}, ErrJournalConflict
		}
		return p.lookup(ctx, o)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return PaperOrder{}, err
	}
	// Asset identity is checked before committing submission authority.
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/assets/"+url.PathEscape(o.Symbol), nil)
	if err != nil {
		return PaperOrder{}, err
	}
	var asset struct {
		Class    string `json:"class"`
		Symbol   string `json:"symbol"`
		Tradable bool   `json:"tradable"`
	}
	if status != 200 || json.Unmarshal(raw, &asset) != nil || asset.Class != "us_equity" || asset.Symbol != o.Symbol || !asset.Tradable {
		return PaperOrder{}, errors.New("paper order requires a tradable US equity")
	}
	if err = p.journal.writeOnce(name, intent); err != nil {
		return PaperOrder{}, err
	}
	if beforePost != nil {
		if err = beforePost(); err != nil {
			// The adapter gate proves no POST started. Preserve both the intent
			// fence and a durable abort so the capped reservation can be released.
			return PaperOrder{}, errors.Join(err, p.writeAbort(o.ClientOrderID))
		}
	}
	raw, status, err = p.request(ctx, http.MethodPost, "/v2/orders", intent)
	if err == nil && status >= 200 && status < 300 {
		if order, decodeErr := decodePaperOrder(raw, o); decodeErr == nil {
			return order, nil
		}
	}
	// Never retry POST, even for a rejection, timeout, truncated response or 5xx.
	return p.lookup(ctx, o)
}

func decodePaperOrder(raw []byte, want StockOrder) (PaperOrder, error) {
	var o PaperOrder
	if json.Unmarshal(raw, &o) != nil || o.ID == "" || o.ClientOrderID != want.ClientOrderID || o.Symbol != want.Symbol || o.Side != want.Side || o.Type != want.Type || o.TimeInForce != want.TimeInForce || !samePaperDecimal(o.Qty, want.Qty) || (want.Type == "limit" && !samePaperDecimal(o.LimitPrice, want.LimitPrice)) || o.Status == "" {
		return PaperOrder{}, errors.New("paper order response does not match retained intent")
	}
	return o, nil
}

func samePaperDecimal(a, b string) bool {
	x, ok := new(big.Rat).SetString(a)
	y, ok2 := new(big.Rat).SetString(b)
	return ok && ok2 && x.Cmp(y) == 0
}

func (p *PaperTrader) lookup(ctx context.Context, o StockOrder) (PaperOrder, error) {
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/orders:by_client_order_id?"+url.Values{"client_order_id": {o.ClientOrderID}}.Encode(), nil)
	if err != nil || status != 200 {
		return PaperOrder{}, ErrSubmissionUnknown
	}
	order, err := decodePaperOrder(raw, o)
	if err != nil {
		return PaperOrder{}, ErrSubmissionUnknown
	}
	return order, nil
}

// Cancel cancels only an order previously declared in this journal.
func (p *PaperTrader) Cancel(ctx context.Context, clientID string) error {
	if !paperID.MatchString(clientID) {
		return errors.New("invalid client order ID")
	}
	if err := p.enter(); err != nil {
		return err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return err
	}
	raw, err := p.journal.read("paper-intent-"+clientID+".json", MaxBytes)
	if err != nil {
		return err
	}
	var intent StockOrder
	if json.Unmarshal(raw, &intent) != nil {
		return errors.New("invalid retained order")
	}
	o, err := p.lookup(ctx, intent)
	if err != nil {
		return err
	}
	_, status, err := p.request(ctx, http.MethodDelete, "/v2/orders/"+url.PathEscape(o.ID), nil)
	if err != nil {
		return err
	}
	if status != 204 {
		return fmt.Errorf("paper cancellation HTTP %d", status)
	}
	return nil
}

// PositionDifference compares exact signed share quantities by venue asset ID.
type PositionDifference struct{ AssetID, Expected, Actual string }

// Reconcile retains raw venue positions and reports all quantity differences.
// Expected quantities must describe the entire account, including other books.
func (p *PaperTrader) Reconcile(ctx context.Context, expected map[string]string) ([]PositionDifference, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return nil, err
	}
	for id, q := range expected {
		if !paperID.MatchString(id) || !validSignedQty(q) {
			return nil, errors.New("invalid expected positions")
		}
	}
	raw, status, err := p.request(ctx, http.MethodGet, "/v2/positions", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("paper positions HTTP %d", status)
	}
	var positions []struct {
		AssetID string `json:"asset_id"`
		Qty     string `json:"qty"`
	}
	if json.Unmarshal(raw, &positions) != nil {
		return nil, errors.New("invalid paper positions")
	}
	actual := map[string]string{}
	for _, pos := range positions {
		if !paperID.MatchString(pos.AssetID) || !validSignedQty(pos.Qty) {
			return nil, errors.New("invalid venue position")
		}
		if _, ok := actual[pos.AssetID]; ok {
			return nil, errors.New("duplicate venue asset")
		}
		actual[pos.AssetID] = pos.Qty
	}
	ids := map[string]bool{}
	for id := range expected {
		ids[id] = true
	}
	for id := range actual {
		ids[id] = true
	}
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	diffs := []PositionDifference{}
	for _, id := range keys {
		want, ok := expected[id]
		if !ok {
			want = "0"
		}
		got, ok := actual[id]
		if !ok {
			got = "0"
		}
		if !samePaperDecimal(want, got) {
			diffs = append(diffs, PositionDifference{id, want, got})
		}
	}
	return diffs, nil
}

func validSignedQty(q string) bool {
	if len(q) > 0 && q[0] == '-' {
		q = q[1:]
	}
	return paperDecimal.MatchString(q)
}

// Fills retains raw account fill activities, with bounded pagination. No fill
// is inferred from order status; callers keep venue fee/quantity fields intact.
func (p *PaperTrader) Fills(ctx context.Context, after time.Time) ([]json.RawMessage, error) {
	if after.IsZero() {
		return nil, errors.New("fills require an explicit lower time bound")
	}
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { <-p.gate }()
	if err := p.bind(ctx); err != nil {
		return nil, err
	}
	var all []json.RawMessage
	token := ""
	for page := 0; page < 100; page++ {
		v := url.Values{"after": {after.UTC().Format(time.RFC3339Nano)}, "direction": {"asc"}, "page_size": {"100"}}
		if token != "" {
			v.Set("page_token", token)
		}
		raw, status, err := p.request(ctx, http.MethodGet, "/v2/account/activities/FILL?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return nil, fmt.Errorf("paper fills HTTP %d", status)
		}
		var fills []json.RawMessage
		if json.Unmarshal(raw, &fills) != nil {
			return nil, errors.New("invalid fill response")
		}
		all = append(all, fills...)
		if len(fills) < 100 {
			return all, nil
		}
		var last struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(fills[len(fills)-1], &last) != nil || last.ID == "" || last.ID == token {
			return nil, errors.New("invalid fill pagination")
		}
		token = last.ID
	}
	return nil, errors.New("fill pagination limit exceeded; narrow the window")
}

// AbortUnsubmitted durably fences a client ID that has no submission intent.
// If Submit won the adapter gate first, the existing intent prevents release.
// If abort wins first, every later Submit for that ID is refused before POST.
func (p *PaperTrader) AbortUnsubmitted(ctx context.Context, clientID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !paperID.MatchString(clientID) {
		return errors.New("invalid client order ID")
	}
	if err := p.enter(); err != nil {
		return err
	}
	defer func() { <-p.gate }()
	if _, err := p.journal.read("paper-abort-"+clientID+".json", MaxBytes); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := p.journal.read("paper-intent-"+clientID+".json", MaxBytes); err == nil {
		return ErrSubmissionUnknown
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return p.writeAbort(clientID)
}

// writeAbort requires the adapter gate and proof that no POST began for this ID.
func (p *PaperTrader) writeAbort(clientID string) error {
	raw, err := json.Marshal(struct{ AccountID, ClientID string }{p.account, clientID})
	if err != nil {
		return err
	}
	return p.journal.writeOnce("paper-abort-"+clientID+".json", raw)
}
