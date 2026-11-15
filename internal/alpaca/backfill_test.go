package alpaca

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestHistoricalPagesResumeAndRejectCorruption(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page_token") == "" {
			fmt.Fprint(w, `{"bars":{"SPY":[]},"next_page_token":"next"}`)
			return
		}
		if calls < 5 {
			w.WriteHeader(503)
			return
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
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	if _, err = j.BackfillDailyBars(context.Background(), c, "SPY", from, to); err == nil {
		t.Fatal("interruption succeeded")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	res, err := j.BackfillDailyBars(context.Background(), c, "SPY", from, to)
	if err != nil || len(res.Pages) != 2 || calls != 5 {
		t.Fatalf("resume: pages=%d calls=%d err=%v", len(res.Pages), calls, err)
	}
	if _, err = j.BackfillDailyBars(context.Background(), c, "SPY", from, to); err != nil || calls != 5 {
		t.Fatal("repeat fetched", calls, err)
	}
	names, err := directoryNames(j.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if len(name) > 8 && name[:8] == "history-" {
			if err := j.root.WriteFile(name, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = j.BackfillDailyBars(context.Background(), c, "SPY", from, to); err == nil || calls != 5 {
		t.Fatal("corrupt cache was used or silently refetched")
	}
}

func TestReadClientOAuthKeepsKeyHeadersAbsent(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer manufactured" || r.Header.Get("APCA-API-KEY-ID") != "" || r.Header.Get("APCA-API-SECRET-KEY") != "" {
			t.Error("wrong authentication mode")
		}
		fmt.Fprint(w, `{"bars":{},"next_page_token":null}`)
	})
	real, err := NewReadClient(PaperCredentials{OAuthToken: "manufactured"}, "sip")
	if err != nil {
		t.Fatal(err)
	}
	c.key, c.secret, c.oauthToken = real.key, real.secret, real.oauthToken
	if _, err := c.FetchDailyBars(context.Background(), []string{"SPY"}, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	for _, creds := range []PaperCredentials{{}, {Key: "key", OAuthToken: "token"}, {OAuthToken: "bad\nvalue"}} {
		if _, err := NewReadClient(creds, "sip"); err == nil {
			t.Fatal("invalid credentials admitted")
		}
	}
}
