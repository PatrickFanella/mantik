package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/loopbacktest"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func newsSupervisionFixture(t *testing.T) (*storage.Memory, storage.NewsCatalog) {
	t.Helper()
	backend := storage.NewMemory()
	catalog, _ := backend.NewsCases(context.Background())
	for i, event := range []string{"one", "two", "three"} {
		r := news.Fixture()
		r.ArticleID = event
		r.Claims[0].EventKey = event
		r.PublishedAt = r.PublishedAt.Add(time.Duration(i) * time.Hour)
		r.AvailableAt = r.AvailableAt.Add(time.Duration(i) * time.Hour)
		r.ReceivedAt = r.ReceivedAt.Add(time.Duration(i) * time.Hour)
		if _, err := backend.AdmitNews(context.Background(), catalog.ReceiverID, r, func() time.Time { return r.ReceivedAt }); err != nil {
			t.Fatal(err)
		}
	}
	catalog, _ = backend.NewsCases(context.Background())
	return backend, catalog
}

func TestNewsSupervisionHTTPJourneyIsReadOnlyAndTargeted(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	handler := service.Handler(backend, "news-view-token")
	t.Setenv("MANTIK_API_TOKEN", "news-view-token")
	var paths []string
	var methods []string
	server := httptest.NewServer(loopbacktest.GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.RequestURI())
		handler.ServeHTTP(w, r)
	})))
	defer server.Close()
	fetch := func(ctx context.Context) (storage.NewsCatalog, error) { return fetchNewsCatalog(ctx, server.URL) }
	inspect := func(ctx context.Context, key string, original bool) (jsonNewsInspection, error) {
		return fetchNewsRecord(ctx, server.URL, key, original)
	}
	a, b := catalog.Cases[0], catalog.Cases[1]
	key := a.RecordKeys[0]
	commands := "d " + b.ID + "\ne " + key + "\nd " + a.ID + "\ne " + key + "\no " + key + "\nn\ne " + key + "\np\nr\no " + key + "\nclose " + a.ID + "\nq\n"
	var out bytes.Buffer
	if err := superviseNews(context.Background(), io.NopCloser(strings.NewReader(commands)), &out, 1, fetch, inspect, nil, nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"/v1/news-cases", "/v1/news-recordings/" + key, "/v1/news-recordings/" + key + "?original=true", "/v1/news-cases"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatal("unexpected fetches", paths)
	}
	if slices.ContainsFunc(methods, func(m string) bool { return m != "GET" }) {
		t.Fatal("mutation request", methods)
	}
	for _, text := range []string{"DISPLAYED NEWS CASE", "CURRENT RECORD EXCERPTS", "CURRENT RECORD WITH EXPLICIT ORIGINAL", "no request sent", "ITEMS 2..2 OF 3", "not_assessed"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal("missing view state", text)
		}
	}
	if strings.Count(out.String(), `"original":`) != 1 {
		t.Fatal("original fetched or printed without explicit command")
	}
	after, _ := backend.NewsCases(context.Background())
	if !reflect.DeepEqual(catalog, after) {
		t.Fatal("viewer mutated service")
	}
}

func TestNewsSupervisionFailureBlocksStaleReads(t *testing.T) {
	_, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	loads, records := 0, 0
	fetch := func(context.Context) (storage.NewsCatalog, error) {
		loads++
		if loads == 2 {
			return storage.NewsCatalog{}, errors.New("offline")
		}
		return catalog, nil
	}
	inspect := func(context.Context, string, bool) (jsonNewsInspection, error) {
		records++
		return jsonNewsInspection{}, errors.New("offline")
	}
	commands := "d " + c.ID + "\ne " + c.LastRecordKey + "\nn\no " + c.LastRecordKey + "\nr\nd " + c.ID + "\nr\nd " + c.ID + "\nq\n"
	var out bytes.Buffer
	if err := superviseNews(context.Background(), io.NopCloser(strings.NewReader(commands)), &out, 1, fetch, inspect, nil, nil); err != nil {
		t.Fatal(err)
	}
	if loads != 3 || records != 1 {
		t.Fatal("stale fetches", loads, records)
	}
	for _, s := range []string{"STALE: news record", "STALE: news catalogue", "STALE: news navigation", "STALE NEWS PAGE"} {
		if !strings.Contains(out.String(), s) {
			t.Fatal("missing stale state", s)
		}
	}
}

func TestNewsCatalogValidationRefusesCorruptOrOversizedState(t *testing.T) {
	_, catalog := newsSupervisionFixture(t)
	for name, mutate := range map[string]func(*storage.NewsCatalog){
		"receiver": func(c *storage.NewsCatalog) { c.ReceiverID = "invalid" }, "schema": func(c *storage.NewsCatalog) { c.Schema = "future" }, "policy": func(c *storage.NewsCatalog) { c.Policy = "other" }, "nil cases": func(c *storage.NewsCatalog) { c.Cases = nil }, "oversize": func(c *storage.NewsCatalog) { c.Cases = make([]newscase.Case, newscase.MaxCases+1) }, "duplicate": func(c *storage.NewsCatalog) { c.Cases[1] = c.Cases[0] }, "unsafe key": func(c *storage.NewsCatalog) { c.Cases[0].RecordKeys[0] = "../escape" }, "version": func(c *storage.NewsCatalog) { c.Cases[0].Version++ }, "latest": func(c *storage.NewsCatalog) { c.Cases[0].LastRecordKey = strings.Repeat("0", 64) }, "independence": func(c *storage.NewsCatalog) { c.Cases[0].Independence = "independent" }, "behavior": func(c *storage.NewsCatalog) { c.Cases[0].Behavior = "execution" }, "status": func(c *storage.NewsCatalog) { c.Cases[0].Status = "qualified" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(catalog)
			var c storage.NewsCatalog
			_ = json.Unmarshal(raw, &c)
			mutate(&c)
			if validateNewsCatalog(c) == nil {
				t.Fatal("corrupt catalogue accepted")
			}
			var out bytes.Buffer
			reads := 0
			err := superviseNews(context.Background(), io.NopCloser(strings.NewReader("n\nd anything\ne anything\nq\n")), &out, 1, func(context.Context) (storage.NewsCatalog, error) { return c, nil }, func(context.Context, string, bool) (jsonNewsInspection, error) {
				reads++
				return jsonNewsInspection{}, nil
			}, nil, nil)
			if err != nil || reads != 0 || strings.Contains(out.String(), "READBACK OK") {
				t.Fatal("invalid catalogue allowed navigation", err)
			}
		})
	}
}

func TestNewsRecordReferenceAndOriginalValidation(t *testing.T) {
	backend, catalog := newsSupervisionFixture(t)
	c := catalog.Cases[0]
	key := c.RecordKeys[0]
	record, _ := backend.NewsRecord(context.Background(), key)
	v := jsonNewsInspection{RecordKey: key, Result: record.Result, Receipt: record.Receipt}
	if validateNewsRecord(v, c, key, false) != nil {
		t.Fatal("valid excerpts rejected")
	}
	v.Original = record.Recording.Original
	if validateNewsRecord(v, c, key, true) != nil {
		t.Fatal("valid original rejected")
	}
	for _, mutate := range []func(*jsonNewsInspection){func(v *jsonNewsInspection) { v.RecordKey = strings.Repeat("0", 64) }, func(v *jsonNewsInspection) { v.Receipt.Matches[0].CaseID = "wrong" }, func(v *jsonNewsInspection) { v.Receipt.Matches[0].Version++ }, func(v *jsonNewsInspection) { v.Result.Claims[0].SourceID = "wrong" }, func(v *jsonNewsInspection) { v.Original += "changed" }, func(v *jsonNewsInspection) { v.Result.Claims[0].Evidence[0].Quote = "wrong" }} {
		raw, _ := json.Marshal(v)
		var bad jsonNewsInspection
		_ = json.Unmarshal(raw, &bad)
		mutate(&bad)
		if validateNewsRecord(bad, c, key, true) == nil {
			t.Fatal("corrupt record accepted")
		}
	}
	if validateNewsRecord(v, c, key, false) == nil {
		t.Fatal("unexpected original accepted")
	}
}

func TestNewsSupervisionEmptyEOFAndCancellation(t *testing.T) {
	backend := storage.NewMemory()
	catalog, _ := backend.NewsCases(context.Background())
	var out bytes.Buffer
	if err := superviseNews(context.Background(), io.NopCloser(strings.NewReader("n\np\nd missing\ne missing\n")), &out, 10, func(context.Context) (storage.NewsCatalog, error) { return catalog, nil }, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ITEMS 0..0 OF 0") {
		t.Fatal("empty page range")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, writer := io.Pipe()
	defer writer.Close()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- superviseNews(ctx, in, io.Discard, 10, func(context.Context) (storage.NewsCatalog, error) { close(ready); return catalog, nil }, nil, nil, nil)
	}()
	<-ready
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("news input goroutine leaked")
	}
}
