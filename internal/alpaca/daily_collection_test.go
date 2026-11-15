package alpaca

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyCollectionRefetchesEmptyPages(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("adjustment") != "raw" {
			t.Error("not raw")
		}
		fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
	})
	c.barInterval = 0
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 2 {
		if _, err = j.CollectDailyBars(context.Background(), c, []string{"SPY"}, start, start.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "daily-*.json"))
	if err != nil || calls != 2 || len(files) != 2 {
		t.Fatal(calls, files, err)
	}
}
