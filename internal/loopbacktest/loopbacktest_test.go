package loopbacktest

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGoClientOnlyIgnoresUnrelatedLocalClients(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(GoClientOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	})))
	defer server.Close()
	for _, agent := range []string{"node", "", "curl/8.0"} {
		r, _ := http.NewRequestWithContext(t.Context(), "GET", server.URL+"/", nil)
		r.Header.Set("User-Agent", agent)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatal(agent, response.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unrelated client reached the fake")
	}
	// The code under test still reaches strict assertions, even when malformed.
	response, err := http.Post(server.URL+"/wrong-path", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTeapot || calls.Load() != 1 {
		t.Fatal("Go client request was filtered", response.StatusCode, calls.Load())
	}
}
