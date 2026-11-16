package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/privatejournal"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// briefNotificationConfig is explicit owner configuration, never server/model
// output. Credentials are environment names, never argv or retained receipt data.
type briefNotificationConfig struct{ URL, TokenEnv string }
type briefRequest struct {
	ServiceURL, BookID, Date string
	AsOf                     time.Time
	Benchmark                *service.BriefBenchmark
	NotificationURL          string
}
type briefReceipt struct {
	Request briefRequest
	Source  service.BriefSource
	Brief   service.DailyBrief
	SHA256  string
}
type briefDelivery struct{ ReceiptSHA256, Destination, IdempotencyKey string }

var briefBookID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var briefEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// briefHTTP never follows redirects or forwards credentials to another origin.
// Cleartext is permitted only to literal loopback addresses for local services.
func briefHTTPURL(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u == nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid brief endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := ip != nil && ip.IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("brief endpoint requires HTTPS or literal loopback HTTP")
	}
	return u, nil
}
func briefHTTP(ctx context.Context, endpoint, token string, body []byte, key string, max int64) ([]byte, error) {
	if _, e := briefHTTPURL(endpoint); e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if e != nil {
		return nil, errors.New("cannot create brief request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(req)
	if e != nil {
		return nil, errors.New("brief request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("brief endpoint returned HTTP %d", response.StatusCode)
	}
	raw, e := io.ReadAll(io.LimitReader(response.Body, max+1))
	if e != nil || int64(len(raw)) > max {
		return nil, errors.New("brief response exceeds bound or is unreadable")
	}
	return raw, nil
}

func loadBriefReceipt(j *privatejournal.Journal, name string, request briefRequest) (briefReceipt, error) {
	var r briefReceipt
	raw, e := j.Read(name, 2*service.MaxBriefSourceBytes)
	if e != nil {
		return r, e
	}
	if e = workerplan.ReadBoundedShape(raw, &r, 2*service.MaxBriefSourceBytes); e != nil {
		return r, e
	}
	want, e := service.BriefHash(request)
	if e != nil {
		return r, e
	}
	got, e := service.BriefHash(r.Request)
	if e != nil {
		return r, e
	}
	if want != got {
		return r, privatejournal.ErrConflict
	}
	// Verify both the frozen inputs and the derived output on every restart.
	rebuilt, e := service.BuildDailyBrief(r.Source, r.Request.Benchmark)
	if e != nil {
		return r, e
	}
	a, e := service.BriefHash(rebuilt)
	if e != nil {
		return r, e
	}
	b, e := service.BriefHash(r.Brief)
	if e != nil {
		return r, e
	}
	if a != b || a != r.SHA256 || r.Source.Book.Config.ID != request.BookID || briefDate(r.Source.AsOf) != request.Date {
		return r, errors.New("invalid retained brief receipt")
	}
	return r, nil
}
func briefDate(at time.Time) string {
	z, _ := time.LoadLocation("America/New_York")
	return at.In(z).Format(time.DateOnly)
}

func freezeBrief(ctx context.Context, j *privatejournal.Journal, name string, request briefRequest, token string) (briefReceipt, error) {
	old, e := loadBriefReceipt(j, name, request)
	if e == nil {
		return old, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return old, e
	}
	body, e := json.Marshal(struct{ AsOf time.Time }{request.AsOf})
	if e != nil {
		return old, e
	}
	endpoint := strings.TrimRight(request.ServiceURL, "/") + "/v1/capped-books/" + request.BookID + "/daily-brief-source"
	raw, e := briefHTTP(ctx, endpoint, token, body, "", service.MaxBriefSourceBytes)
	if e != nil {
		return old, e
	}
	var source service.BriefSource
	if e = workerplan.ReadBoundedShape(raw, &source, service.MaxBriefSourceBytes); e != nil {
		return old, e
	}
	if source.Book.Config.ID != request.BookID || briefDate(source.AsOf) != request.Date || !request.AsOf.IsZero() && !source.AsOf.Equal(request.AsOf) {
		return old, errors.New("brief source differs from requested book or date")
	}
	b, e := service.BuildDailyBrief(source, request.Benchmark)
	if e != nil {
		return old, e
	}
	hash, e := service.BriefHash(b)
	if e != nil {
		return old, e
	}
	receipt := briefReceipt{Request: request, Source: source, Brief: b, SHA256: hash}
	raw, e = json.Marshal(receipt)
	if e != nil {
		return old, e
	}
	if e = j.WriteOnce(name, raw, 2*service.MaxBriefSourceBytes); e != nil {
		return old, e
	}
	return receipt, nil
}

func deliverBrief(ctx context.Context, j *privatejournal.Journal, name string, r briefReceipt, c briefNotificationConfig) error {
	if c.URL == "" || c.URL != r.Request.NotificationURL {
		return errors.New("notification requires the receipt's explicitly configured destination")
	}
	key, e := service.BriefHash(struct{ Book, Date, Hash string }{r.Request.BookID, r.Request.Date, r.SHA256})
	if e != nil {
		return e
	}
	d := briefDelivery{r.SHA256, c.URL, "mantik-brief-" + key}
	raw, e := json.Marshal(d)
	if e != nil {
		return e
	}
	// Persist intent BEFORE HTTP. A failed/ambiguous delivery leaves the exact body
	// and key available after restart; destination changes conflict.
	if e = j.WriteOnce(name+"-intent.json", raw, 4096); e != nil {
		return e
	}
	if ack, e := j.Read(name+"-delivered.json", 4096); e == nil {
		if !bytes.Equal(ack, raw) {
			return privatejournal.ErrConflict
		}
		return nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	token := ""
	if c.TokenEnv != "" {
		if !briefEnv.MatchString(c.TokenEnv) {
			return errors.New("invalid notification token environment name")
		}
		token = os.Getenv(c.TokenEnv)
		if token == "" {
			return errors.New("notification token environment is empty")
		}
	}
	var text bytes.Buffer
	if e = service.RenderDailyBrief(&text, r.Brief); e != nil {
		return e
	}
	payload, e := json.Marshal(struct {
		ID, ReceiptSHA256, Text string
		Brief                   service.DailyBrief
	}{d.IdempotencyKey, r.SHA256, text.String(), r.Brief})
	if e != nil {
		return e
	}
	if _, e = briefHTTP(ctx, c.URL, token, payload, d.IdempotencyKey, 4096); e != nil {
		return e
	}
	return j.WriteOnce(name+"-delivered.json", raw, 4096)
}

func runDailyBrief(ctx context.Context, args []string, out, errOut io.Writer) (err error) {
	fs := flag.NewFlagSet("daily-brief", flag.ContinueOnError)
	fs.SetOutput(errOut)
	server := fs.String("url", defaultURL, "authenticated Mantik service URL")
	tokenEnv := fs.String("token-env", "MANTIK_API_TOKEN", "owner API credential environment name")
	book := fs.String("book", "", "retained capped book ID")
	date := fs.String("date", "", "daily identity YYYY-MM-DD in America/New_York")
	asof := fs.String("as-of", "", "optional RFC3339 valuation clock; required to match imported SPY evidence")
	journal := fs.String("outbox", "", "absolute private daily brief receipt/outbox directory")
	benchmark := fs.String("benchmark", "", "optional retained SPY total-return index evidence JSON")
	config := fs.String("notification-config", "", "explicit owner mode-0600 JSON with URL and optional TokenEnv")
	notify := fs.Bool("notify", false, "deliver the frozen receipt to the configured destination")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || !briefBookID.MatchString(*book) || !briefEnv.MatchString(*tokenEnv) || *journal == "" {
		return errors.New("daily-brief requires book, date, outbox and a token environment name")
	}
	if d, e := time.Parse(time.DateOnly, *date); e != nil || d.Format(time.DateOnly) != *date {
		return errors.New("daily-brief requires a valid explicit date")
	}
	u, e := briefHTTPURL(*server)
	if e != nil {
		return e
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("Mantik service URL must have a root path")
	}
	request := briefRequest{ServiceURL: strings.TrimRight(*server, "/"), BookID: *book, Date: *date}
	if *asof != "" {
		request.AsOf, e = time.Parse(time.RFC3339Nano, *asof)
		if e != nil {
			return errors.New("invalid brief as-of clock")
		}
	}
	if *benchmark != "" {
		var b service.BriefBenchmark
		if e = readGateJSON(*benchmark, &b, 16384); e != nil {
			return e
		}
		request.Benchmark = &b
	}
	var c briefNotificationConfig
	if *config != "" {
		if e = readPaperJSON(*config, &c, true); e != nil {
			return e
		}
		if _, e = briefHTTPURL(c.URL); e != nil {
			return e
		}
		request.NotificationURL = c.URL
	}
	if *notify && c.URL == "" {
		return errors.New("notify requires an explicit notification-config")
	}
	token := os.Getenv(*tokenEnv)
	// Existing receipts can render/retry while the API is down. New reads need auth.
	j, e := privatejournal.Open(*journal, true, "mantik-daily-brief-outbox-v1", "brief-")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, j.Close()) }()
	name := *book + "-" + *date
	r, e := loadBriefReceipt(j, name+".json", request)
	if errors.Is(e, os.ErrNotExist) {
		if token == "" {
			return errors.New("owner API token environment is empty")
		}
		r, e = freezeBrief(ctx, j, name+".json", request, token)
	}
	if e != nil {
		return e
	}
	if e = service.RenderDailyBrief(out, r.Brief); e != nil {
		return e
	}
	if *notify {
		return deliverBrief(ctx, j, name, r, c)
	}
	return nil
}
