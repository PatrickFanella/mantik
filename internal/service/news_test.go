package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestNewsAPI(t *testing.T) {
	s := storage.NewMemory()
	ctx := context.Background()
	r := news.Fixture()
	clock := &fakeClock{at: r.ReceivedAt.Add(time.Hour)}
	h := HandlerWithClock(s, "private-test-token", clock)
	call := func(method, path string, body []byte, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer private-test-token")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := call("GET", "/v1/news-cases", nil, false); w.Code != 401 {
		t.Fatal("unauthenticated read", w.Code)
	}
	catalog, _ := s.NewsCases(ctx)
	path := "/v1/news-recordings?receiver_id=" + catalog.ReceiverID
	raw, _ := json.Marshal(r)
	if w := call("POST", path, raw, false); w.Code != 401 {
		t.Fatal("unauthenticated mutation", w.Code)
	}
	if w := call("POST", "/v1/news-recordings?receiver_id=receiver-abcdefghijklmnop", raw, true); w.Code != 409 {
		t.Fatal("receiver mismatch", w.Code)
	}
	w := call("POST", path, raw, true)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var receipt newscase.Receipt
	_ = json.Unmarshal(w.Body.Bytes(), &receipt)
	if w = call("POST", path, raw, true); w.Code != 200 {
		t.Fatal("retry", w.Code)
	}
	r.Claims[0].Assertion = "changed"
	changed, _ := json.Marshal(r)
	if w = call("POST", path, changed, true); w.Code != 409 {
		t.Fatal("conflict", w.Code)
	}
	recordPath := "/v1/news-recordings/" + receipt.RecordKey
	w = call("GET", recordPath, nil, true)
	var view map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if w.Code != 200 || view["original"] != nil || view["result"] == nil || view["receipt"] == nil {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("GET", recordPath+"?original=true", nil, true)
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	var original string
	_ = json.Unmarshal(view["original"], &original)
	if w.Code != 200 || original != news.Fixture().Original {
		t.Fatal("original changed", w.Body.String())
	}
	for _, query := range []string{"?arbitrary=true", "?original=maybe", "?original=true&original=false"} {
		if w = call("GET", recordPath+query, nil, true); w.Code != 422 {
			t.Fatal(query, w.Code)
		}
	}
	if w = call("POST", path, bytes.Replace(raw, []byte(`"schema":`), []byte(`"schema":"duplicate","schema":`), 1), true); w.Code != 422 {
		t.Fatal("duplicate source JSON accepted", w.Code)
	}
	if w = call("POST", path, bytes.Repeat([]byte("x"), news.MaxRecordingBytes+1), true); w.Code != 422 {
		t.Fatal("oversized body accepted", w.Code)
	}
	closeBody, _ := json.Marshal(map[string]any{"receiver_id": catalog.ReceiverID, "version": 1})
	closePath := "/v1/news-cases/" + receipt.Matches[0].CaseID + "/close"
	if w = call("POST", closePath, closeBody, true); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	next := news.Fixture()
	next.SourceID = "syndicated"
	raw, _ = json.Marshal(next)
	if w = call("POST", path, raw, true); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = call("POST", closePath, closeBody, true); w.Code != 409 {
		t.Fatal("stale closure accepted", w.Code)
	}
	w = call("GET", "/v1/news-cases", nil, true)
	var actual storage.NewsCatalog
	_ = json.Unmarshal(w.Body.Bytes(), &actual)
	if w.Code != 200 || len(actual.Cases) != 1 || actual.Cases[0].Version != 2 || actual.Cases[0].Status != "open" {
		t.Fatal(w.Body.String())
	}
}
