package polygonref

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
)

const testKey = "manufactured-polygon-key"

func dividendSpec(id string) Spec {
	return Spec{ID: id, Method: MethodDividends, Symbol: "SPY", Start: "2026-01-01", End: "2026-10-30", AccessRef: "manufactured-polygon-account"}
}

func dividendRecord(id, date, amount string) string {
	return fmt.Sprintf(`{"cash_amount":%s,"currency":"USD","declaration_date":"2026-01-02","dividend_type":"CD","ex_dividend_date":%q,"frequency":4,"id":%q,"pay_date":"2026-10-30","record_date":%q,"ticker":"SPY"}`, amount, date, id, date)
}

func pageBody(next string, records ...string) []byte {
	if next != "" {
		next = fmt.Sprintf(`,"next_url":%q`, next)
	}
	return fmt.Appendf(nil, `{"results":[%s],"status":"OK","request_id":"manufactured"%s}`, strings.Join(records, ","), next)
}

func TestParseKeepsExactProviderTextAndFailsClosed(t *testing.T) {
	s := dividendSpec("parse")
	body := pageBody("", dividendRecord("Eb2", "2026-09-18", "1.888834"), dividendRecord("Ea1", "2026-03-20", "1.796999"))
	snap, err := Parse(s, [][]byte{body})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Dividends) != 2 || snap.Dividends[0].ExDividendDate != "2026-03-20" || snap.Dividends[1].CashAmount != "1.888834" || snap.Dividends[1].DividendType != "CD" || snap.Dividends[1].PayDate != "2026-10-30" || snap.ProviderAttested || snap.CoverageComplete || snap.EconomicApplicationReady {
		t.Fatalf("%+v", snap)
	}
	splits := Spec{ID: "splits", Method: MethodSplits, Symbol: "AAPL", Start: "2020-01-01", End: "2020-12-31", AccessRef: "x"}
	snapSplits, err := Parse(splits, [][]byte{pageBody("", `{"execution_date":"2020-08-31","id":"P1","split_from":1,"split_to":4,"ticker":"AAPL"}`)})
	if err != nil || len(snapSplits.Splits) != 1 || snapSplits.Splits[0].SplitTo != "4" || snapSplits.Splits[0].SplitFrom != "1" {
		t.Fatal(snapSplits, err)
	}
	for name, bodies := range map[string][][]byte{
		"other ticker":     {[]byte(strings.Replace(string(body), `"ticker":"SPY"`, `"ticker":"SPYG"`, 1))},
		"outside range":    {pageBody("", dividendRecord("Ea1", "2025-12-31", "1"))},
		"duplicate id":     {pageBody("", dividendRecord("Ea1", "2026-03-20", "1"), dividendRecord("Ea1", "2026-06-18", "1"))},
		"exponent amount":  {pageBody("", dividendRecord("Ea1", "2026-03-20", "1e-3"))},
		"zero amount":      {pageBody("", dividendRecord("Ea1", "2026-03-20", "0.000"))},
		"string amount":    {pageBody("", dividendRecord("Ea1", "2026-03-20", `"1.0"`))},
		"status not ok":    {[]byte(`{"results":[],"status":"ERROR"}`)},
		"missing results":  {[]byte(`{"status":"OK"}`)},
		"trailing data":    {append(pageBody(""), []byte(`{}`)...)},
		"unfinished chain": {pageBody("https://api.polygon.io/v3/reference/dividends?cursor=abc")},
		"early terminal":   {pageBody(""), pageBody("")},
		"bad pay date":     {[]byte(strings.Replace(string(body), `"pay_date":"2026-10-30"`, `"pay_date":"10/30/2026"`, 1))},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Parse(s, bodies); !errors.Is(err, ErrUnparseable) {
				t.Fatal("malformed snapshot accepted", got, err)
			}
		})
	}
}

func TestNextURLAcceptsOnlySamePathCursor(t *testing.T) {
	first := dividendSpec("x").FirstURL()
	if got, err := NextURL(first, "https://api.polygon.io/v3/reference/dividends?cursor=YXA9MSZh"); err != nil || got != Origin+"/v3/reference/dividends?cursor=YXA9MSZh" {
		t.Fatal(got, err)
	}
	for _, bad := range []string{
		"http://api.polygon.io/v3/reference/dividends?cursor=a",
		"https://evil.example/v3/reference/dividends?cursor=a",
		"https://api.polygon.io/v3/reference/splits?cursor=a",
		"https://api.polygon.io/v3/reference/dividends?cursor=a&apiKey=leaked",
		"https://api.polygon.io/v3/reference/dividends?ticker=SPY",
		"https://user@api.polygon.io/v3/reference/dividends?cursor=a",
	} {
		if _, err := NextURL(first, bad); err == nil {
			t.Fatal("continuation accepted", bad)
		}
	}
}

type fakePolygon struct {
	calls  atomic.Int32
	status atomic.Int32
	pages  map[string][]byte
}

func newFakePolygon(t *testing.T, pages map[string][]byte) (*fakePolygon, string) {
	t.Helper()
	f := &fakePolygon{pages: pages}
	f.status.Store(http.StatusOK)
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		q := r.URL.Query()
		if r.Method != http.MethodGet || q.Get("apiKey") != testKey {
			t.Error("request escaped its read-only credentialed contract", r.Method)
		}
		q.Del("apiKey")
		if status := int(f.status.Load()); status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		body, ok := f.pages[r.URL.Path+"?"+q.Encode()]
		if !ok {
			t.Error("unexpected query", r.URL.Path, q.Encode())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	})))
	t.Cleanup(server.Close)
	return f, server.URL
}

func testJournal(t *testing.T, origin string) (*Journal, string, *atomic.Int32) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "polygon-journal")
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	var pauses atomic.Int32
	j.origin = origin
	j.pause = func(_ context.Context, d time.Duration) error {
		if d != pageInterval {
			t.Error("page pacing changed", d)
		}
		pauses.Add(1)
		return nil
	}
	return j, dir, &pauses
}

func firstQuery(s Spec) string { return strings.TrimPrefix(s.FirstURL(), Origin) }

func TestCollectFollowsCursorChainWithoutRetainingKey(t *testing.T) {
	s := dividendSpec("spy-dividends")
	next := "https://api.polygon.io/v3/reference/dividends?cursor=page2"
	fake, origin := newFakePolygon(t, map[string][]byte{
		firstQuery(s):                          pageBody(next, dividendRecord("Ea1", "2026-03-20", "1.796999")),
		"/v3/reference/dividends?cursor=page2": pageBody("", dividendRecord("Eb2", "2026-09-18", "1.888834")),
	})
	j, dir, pauses := testJournal(t, origin)
	got, err := j.Collect(context.Background(), s, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "retained" || len(got.Pages) != 2 || got.Pages[1].RequestURL != Origin+"/v3/reference/dividends?cursor=page2" || got.ParseStatus != "parsed" || len(got.Snapshot.Dividends) != 2 || pauses.Load() != 1 || got.CoverageComplete {
		t.Fatalf("%+v", got)
	}
	again, err := j.Collect(context.Background(), s, testKey)
	if err != nil || fake.calls.Load() != 2 || !reflect.DeepEqual(again, got) {
		t.Fatal("completed identity refetched", err, fake.calls.Load())
	}
	changed := s
	changed.End = "2026-10-29"
	if _, err := j.Collect(context.Background(), changed, testKey); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("changed spec reused an identity", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil || strings.Contains(string(raw), testKey) {
			t.Fatal("credential retained in journal artifact", e.Name(), err)
		}
	}
	body := filepath.Join(dir, bodyName(s.ID, 2))
	raw, _ := os.ReadFile(body)
	if err := os.Remove(body); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(body, []byte(strings.Replace(string(raw), "1.888834", "1.888835", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Collection(s.ID); !errors.Is(err, ErrJournalConflict) {
		t.Fatal("altered provider body accepted", err)
	}
}

func TestCollectFailuresAreRetainedAndNeverResumed(t *testing.T) {
	s := dividendSpec("limited")
	fake, origin := newFakePolygon(t, map[string][]byte{firstQuery(s): pageBody("")})
	fake.status.Store(http.StatusTooManyRequests)
	j, _, _ := testJournal(t, origin)
	got, err := j.Collect(context.Background(), s, testKey)
	if err == nil || got.State != "failed" || got.HTTPStatus != http.StatusTooManyRequests || strings.Contains(err.Error(), testKey) {
		t.Fatalf("%+v %v", got, err)
	}
	fake.status.Store(http.StatusOK)
	if _, err := j.Collect(context.Background(), s, testKey); !errors.Is(err, ErrCollectionUnresolved) || fake.calls.Load() != 1 {
		t.Fatal("failed identity retried", err)
	}
	escaping := dividendSpec("escaping")
	_, origin = newFakePolygon(t, map[string][]byte{firstQuery(escaping): pageBody("https://evil.example/v3/reference/dividends?cursor=x")})
	j, dir, _ := testJournal(t, origin)
	got, err = j.Collect(context.Background(), escaping, testKey)
	if err == nil || got.FailureReason != "invalid_continuation" || len(got.Pages) != 0 {
		t.Fatalf("escaping continuation followed: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, bodyName("escaping", 1))); !os.IsNotExist(err) {
		t.Fatal("rejected page retained as evidence")
	}
	if _, err := j.Collect(context.Background(), dividendSpec("no-key"), ""); err == nil {
		t.Fatal("collection started without a credential")
	}
}

func TestSpecBounds(t *testing.T) {
	for _, s := range []Spec{
		{},
		{ID: "x", Method: "other", Symbol: "SPY", Start: "2026-01-01", End: "2026-01-02", AccessRef: "x"},
		{ID: "x", Method: MethodDividends, Symbol: "spy", Start: "2026-01-01", End: "2026-01-02", AccessRef: "x"},
		{ID: "x", Method: MethodDividends, Symbol: "SPY", Start: "2026-01-02", End: "2026-01-01", AccessRef: "x"},
		{ID: "x", Method: MethodDividends, Symbol: "SPY", Start: "2024-01-01", End: "2026-01-01", AccessRef: "x"},
		{ID: "x", Method: MethodDividends, Symbol: "SPY", Start: "2026-02-30", End: "2026-03-01", AccessRef: "x"},
	} {
		if s.Validate() == nil {
			t.Fatal("invalid spec accepted", s)
		}
	}
}
