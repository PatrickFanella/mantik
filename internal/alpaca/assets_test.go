package alpaca

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestAssetListsRetainActiveAndInactiveAndLargeReferences(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query()
		status := q.Get("status")
		if r.URL.Path != "/v2/assets" || q.Get("asset_class") != "us_equity" || (status != "active" && status != "inactive") {
			t.Error(r.URL)
		}
		// Real lists exceed the normal data-page bound. Unknown source fields
		// remain in retained raw chunks while normalized fields stay bounded.
		fmt.Fprintf(w, `[{"id":%q,"symbol":"SPY","class":"us_equity","exchange":"ARCA","status":%q,"tradable":false,"unknown":%q}]`, status, status, strings.Repeat("x", MaxBytes+1))
	})
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	snapshot, err := j.StockAssets(context.Background(), c)
	if err != nil || calls != 2 || len(snapshot.Assets) != 2 || snapshot.ObservedAt.IsZero() {
		t.Fatal(snapshot.ObservedAt, calls, err)
	}
	names, err := directoryNames(j.root)
	if err != nil {
		t.Fatal(err)
	}
	manifests, chunks := 0, 0
	for _, name := range names {
		if strings.HasPrefix(name, "asset-list-") {
			manifests++
		}
		if strings.HasPrefix(name, "asset-bytes-") {
			chunks++
		}
	}
	if manifests != 2 || chunks < 2 {
		t.Fatal("raw reference not retained", manifests, chunks)
	}
}
