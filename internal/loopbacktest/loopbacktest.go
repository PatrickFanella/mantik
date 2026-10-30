// Package loopbacktest isolates test HTTP fakes from unrelated local clients.
//
// Other processes on a developer workstation can connect to a loopback test
// server; T3 port detection sends `GET /` with a Node.js user agent shortly
// after a listener opens. Mantik's clients use Go's default user agent, so
// fakes can reject other clients before counting requests or asserting the
// boundary contract of the code under test. Import this only from tests.
package loopbacktest

import (
	"context"
	"net/http"
	"strings"
)

// GoClientOnly answers requests from non-Go clients with 404 and passes every
// Go client request, including malformed ones, to h unchanged.
func GoClientOnly(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Go-http-client/") {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// Probe sends the request shape observed from T3 port detection and returns
// the response status, so tests can assert that a fake ignored it.
func Probe(ctx context.Context, url string) (int, error) {
	r, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(url, "/")+"/", nil)
	if err != nil {
		return 0, err
	}
	r.Header.Set("User-Agent", "node")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}
