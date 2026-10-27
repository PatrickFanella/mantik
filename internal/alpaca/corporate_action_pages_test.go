package alpaca

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func actionPage(id, next string) string {
	var token any
	if next != "" {
		token = next
	}
	raw, _ := json.Marshal(map[string]any{"corporate_actions": map[string]any{"cash_dividends": []any{map[string]any{"id": id, "symbol": "SPY", "rate": 0.125}}}, "next_page_token": token})
	return string(raw)
}
func corporatePageSpec(id, token string) CollectionSpec {
	s := collectionSpec()
	s.ID = id
	s.Capability = "corporate_actions"
	s.Start = "2026-10-14"
	s.End = s.Start
	s.PageToken = token
	return s
}
func TestCorporateActionRetainedPageChainHistoricalCutsRestoreAndAmbiguity(t *testing.T) {
	ctx := context.Background()
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	cursor := "cursor+/=&? space"
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		token := r.URL.Query().Get("page_token")
		switch token {
		case "":
			_, _ = fmt.Fprint(w, actionPage("a", cursor))
		case cursor:
			_, _ = fmt.Fprint(w, actionPage("b", "last"))
		case "last":
			_, _ = fmt.Fprint(w, actionPage("c", ""))
		default:
			t.Error("unexpected cursor", token)
		}
	})
	first := corporatePageSpec("root", "")
	if _, err := j.Collect(ctx, c, first); err != nil {
		t.Fatal(err)
	}
	rootSnapshots, err := j.CorporateActionSnapshots(time.Now().UTC())
	if err != nil || len(rootSnapshots) != 1 {
		t.Fatal(rootSnapshots, err)
	}
	cut := rootSnapshots[0].AvailableAt
	before, err := j.CorporateActionPageChain(first.ID, 1, cut.Add(-time.Nanosecond))
	if err != nil || before.Status != "root_unavailable_at_cut" {
		t.Fatal(before, err)
	}
	early, err := j.CorporateActionPageChain(first.ID, 1, cut)
	if err != nil || early.Status != "next_page_unavailable_at_cut" || len(early.Pages) != 1 || calls.Load() != 1 {
		t.Fatal(early, err)
	}
	second := corporatePageSpec("middle", cursor)
	if _, err := j.Collect(ctx, c, second); err != nil {
		t.Fatal(err)
	}
	third := corporatePageSpec("end", "last")
	if _, err := j.Collect(ctx, c, third); err != nil {
		t.Fatal(err)
	}
	got, err := j.CorporateActionPageChain(first.ID, 1, time.Now().UTC())
	if err != nil || got.Status != "page_chain_terminated" || !got.PaginationTerminated || len(got.Pages) != 3 || got.RecordsObserved != 3 || got.CoverageComplete || got.ProviderAtomicityProven || got.EconomicApplicationReady || got.Authority != "journal_owned_source_page_links_only" {
		t.Fatal(got, err)
	}
	for n, p := range got.Pages {
		if p.RequestedAt == nil || p.RequestedAt.After(p.ReceivedAt) || n > 0 && !p.RequestedAt.After(got.Pages[n-1].AvailableAt) {
			t.Fatal("noncausal chain", got)
		}
	}
	if got.Pages[1].PageToken != cursor || got.Pages[2].PageToken != "last" || got.Pages[0].PageToken != "" {
		t.Fatal("query cursors lost")
	}
	if _, err := j.Collect(ctx, c, second); err != nil || calls.Load() != 3 {
		t.Fatal("completed page refetched", err)
	}
	unchanged, err := j.CorporateActionPageChain(first.ID, 1, cut)
	if err != nil || !reflect.DeepEqual(unchanged, early) {
		t.Fatal("future page leaked into source cut", err)
	}
	archiveDir := filepath.Join(t.TempDir(), "archive")
	a, err := j.Archive(ctx, archiveDir)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "restored")
	restored, err := RestoreArchive(ctx, archiveDir, a.SHA256, destination)
	if err != nil || !restored.ContractsVerified || !restored.BytesVerified {
		t.Fatal(restored, err)
	}
	rj := testOpenJournal(t, destination)
	replay, err := rj.CorporateActionPageChain(first.ID, 1, got.AsOf)
	if err != nil || !reflect.DeepEqual(replay, got) {
		t.Fatal("restored chain differs", replay, err)
	}
	// A later repeated cursor acquisition stays visible and cannot silently
	// replace a prior page, even when the returned source objects are identical.
	duplicate := corporatePageSpec("repeat-middle", cursor)
	if _, err := j.Collect(ctx, c, duplicate); err != nil {
		t.Fatal(err)
	}
	ambiguous, err := j.CorporateActionPageChain(first.ID, 1, time.Now().UTC())
	if err != nil || ambiguous.Status != "ambiguous_next_page" || len(ambiguous.Pages) != 1 || len(ambiguous.CandidateCaptures) != 2 || ambiguous.PaginationTerminated {
		t.Fatal(ambiguous, err)
	}
	// Rehashing a changed capture/query projection cannot evade the original
	// collection intent comparison.
	captures, err := rj.captures()
	if err != nil {
		t.Fatal(err)
	}
	var target capture
	for _, x := range captures {
		if x.Recording.CaptureID == second.ID {
			target = x
		}
	}
	target.Recording.PageToken = "forged"
	target.Result.CorporateActions.PageToken = "forged"
	target.SHA256, _ = captureDigest(target)
	raw, _ := json.Marshal(target)
	if err := os.WriteFile(filepath.Join(destination, captureName(target.Recording)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := rj.CorporateActionPageChain(first.ID, 1, got.AsOf); err == nil {
		t.Fatal("forged retained request accepted")
	}
}

func TestCorporateActionPageChainStopsAtCyclesOverlapsAndChangedScopes(t *testing.T) {
	for _, kind := range []string{"cycle", "overlap", "scope", "interrupted", "manual"} {
		t.Run(kind, func(t *testing.T) {
			j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
			var calls int
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("page_token") == "" {
					_, _ = fmt.Fprint(w, actionPage("a", "next"))
					return
				}
				id, next := "b", ""
				if kind == "cycle" {
					next = "next"
				}
				if kind == "overlap" {
					id = "a"
				}
				_, _ = fmt.Fprint(w, actionPage(id, next))
			})
			root := corporatePageSpec("root", "")
			if _, err := j.Collect(context.Background(), c, root); err != nil {
				t.Fatal(err)
			}
			page := corporatePageSpec("page", "next")
			if kind == "scope" {
				page.AccessRef = "different-source-access"
			}
			if kind == "manual" {
				record := corporateRecording(actionPage("b", ""))
				record.CaptureID = "manual-page"
				record.EvidenceClass = "prospective_market_unqualified"
				record.AccessRef = root.AccessRef
				record.PageToken = "next"
				record.ReceivedAt = time.Now().UTC()
				if _, err := j.Retain(record); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := j.Collect(context.Background(), c, page); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "interrupted" {
				if err := os.Remove(filepath.Join(j.root.Name(), collectionName(page.ID, "complete"))); err != nil {
					t.Fatal(err)
				}
			}
			got, err := j.CorporateActionPageChain(root.ID, 1, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"cycle": "cursor_cycle", "overlap": "overlapping_source_ids", "scope": "next_page_unavailable_at_cut", "interrupted": "next_page_unavailable_at_cut", "manual": "next_page_acquisition_not_causal"}[kind]
			if got.Status != want || got.PaginationTerminated || got.CoverageComplete || got.EconomicApplicationReady {
				t.Fatal(got, want)
			}
		})
	}
}

func TestCorporateActionPageTokenCannotEscapeCapabilityOrBounds(t *testing.T) {
	c := testClient(t, func(http.ResponseWriter, *http.Request) { t.Error("invalid cursor reached HTTP") })
	for _, q := range []Query{{Capability: "latest_quotes", Symbols: []string{"SPY"}, PageToken: "next"}, {Capability: "market_calendar", Start: "2026-10-14", End: "2026-10-14", PageToken: "next"}, {Capability: "corporate_actions", Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", PageToken: strings.Repeat("a", 4097)}, {Capability: "corporate_actions", Symbols: []string{"SPY"}, Start: "2026-10-14", End: "2026-10-14", PageToken: "bad\n"}} {
		if _, err := c.Fetch(context.Background(), q); err == nil {
			t.Fatal("invalid query accepted", q.Capability)
		}
	}
	for _, capability := range []string{"latest_quotes", "market_calendar"} {
		r := recording(goodQuotes)
		r.Capability = capability
		r.PageToken = "next"
		if _, err := Normalize(r); err == nil {
			t.Fatal("recording widened old capability")
		}
	}
}

func TestCorporateActionPageChainBoundsAndPriorAcquisition(t *testing.T) {
	j := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	n := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = fmt.Fprint(w, actionPage(fmt.Sprintf("record-%d", n), fmt.Sprintf("token-%d", n)))
	})
	for index := 0; index < MaxCorporateActionChainPages+1; index++ {
		token := ""
		if index > 0 {
			token = fmt.Sprintf("token-%d", index)
		}
		if _, err := j.Collect(context.Background(), c, corporatePageSpec(fmt.Sprintf("page-%d", index), token)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := j.CorporateActionPageChain("page-0", 1, time.Now().UTC())
	if err != nil || got.Status != "page_bound_reached" || len(got.Pages) != MaxCorporateActionChainPages || got.PaginationTerminated {
		t.Fatal(got, err)
	}
	// An actual completed page with the matching cursor but acquired before
	// this later first page is not a causal continuation of that page.
	j2 := testOpenJournal(t, filepath.Join(t.TempDir(), "journal"))
	c2 := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page_token") == "" {
			_, _ = fmt.Fprint(w, actionPage("a", "next"))
			return
		}
		_, _ = fmt.Fprint(w, actionPage("b", ""))
	})
	if _, err := j2.Collect(context.Background(), c2, corporatePageSpec("prior", "next")); err != nil {
		t.Fatal(err)
	}
	if _, err := j2.Collect(context.Background(), c2, corporatePageSpec("later-root", "")); err != nil {
		t.Fatal(err)
	}
	prior, err := j2.CorporateActionPageChain("later-root", 1, time.Now().UTC())
	if err != nil || prior.Status != "next_page_acquisition_not_causal" || prior.PaginationTerminated {
		t.Fatal(prior, err)
	}
	if _, err := j2.CorporateActionPageChain("prior", 1, time.Now().UTC()); err == nil {
		t.Fatal("non-first page used as root")
	}
}
