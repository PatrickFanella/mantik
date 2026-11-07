package service

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// One portfolio evaluation inspects every outcome's market evidence, and every
// outcome lists the same retained quote rows. A row's session evidence
// depends only on what its feed retained up to the row's availability cut, so
// one evaluation reuses it across outcomes. The key binds digests of every
// record published and every admission made by the cut, not an assertion that
// outcome feeds share a history.
type sessionMemo struct {
	mu      sync.Mutex
	entries map[string]PaperOutcomeSessionEvidence
}

type sessionMemoKey struct{}

func withSessionMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, sessionMemoKey{}, &sessionMemo{entries: map[string]PaperOutcomeSessionEvidence{}})
}

func sessionMemoFrom(ctx context.Context) *sessionMemo {
	m, _ := ctx.Value(sessionMemoKey{}).(*sessionMemo)
	return m
}

type feedPrefixPoint struct {
	at     time.Time
	digest string
}

// feedPrefixes chains digests over one snapshot's records by publication clock
// and over one registration's admissions, with their routed inputs, by
// admission clock. Records of every registration are bound because the
// session cut reads the whole record map.
type feedPrefixes struct {
	registration string
	records      []feedPrefixPoint
	admissions   []feedPrefixPoint
}

func newFeedPrefixes(feed storage.FeedEvidenceSnapshot, registration string) feedPrefixes {
	entry, ok := feed.Collectors.Entries[registration]
	out := feedPrefixes{registration: workerplan.Hash(struct {
		ID           string
		Present      bool
		Registration feedcollector.Registration
	}{registration, ok, entry.Registration})}
	keys := make([]string, 0, len(feed.Records))
	for key := range feed.Records {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := feed.Records[a].PublishedAt.Compare(feed.Records[b].PublishedAt); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	previous := ""
	for _, key := range keys {
		r := feed.Records[key]
		previous = workerplan.Hash(struct {
			Previous, Key string
			Record        storage.FeedEvidenceRecord
		}{previous, key, r})
		out.records = append(out.records, feedPrefixPoint{r.PublishedAt, previous})
	}
	ids := []string{}
	for id, a := range feed.Collectors.Admissions {
		if a.Fence.RegistrationID == registration {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := feed.Collectors.Admissions[a].At.Compare(feed.Collectors.Admissions[b].At); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	previous = ""
	for _, id := range ids {
		var retained domain.RetainedRoutingInput
		routed := false
		if feed.Routing != nil {
			retained, routed = feed.Routing.Inputs[id]
		}
		a := feed.Collectors.Admissions[id]
		previous = workerplan.Hash(struct {
			Previous, ID string
			Admission    feedcollector.Admission
			Routed       bool
			Input        domain.RetainedRoutingInput
		}{previous, id, a, routed, retained})
		out.admissions = append(out.admissions, feedPrefixPoint{a.At, previous})
	}
	return out
}

func prefixDigest(points []feedPrefixPoint, cut time.Time) string {
	n, _ := slices.BinarySearchFunc(points, cut, func(p feedPrefixPoint, cut time.Time) int {
		if p.at.After(cut) {
			return 1
		}
		return -1
	})
	if n == 0 {
		return ""
	}
	return points[n-1].digest
}

// inspect returns inspectOutcomeQuoteSession's result for row, computing it
// at most once per distinct retained cut in this evaluation.
func (m *sessionMemo) inspect(ctx context.Context, prefixes feedPrefixes, feed storage.FeedEvidenceSnapshot, registration string, row PaperOutcomeMarketRow) (PaperOutcomeSessionEvidence, error) {
	key := workerplan.Hash(struct {
		Registration, Records, Admissions string
		InputID, CaptureKey               string
		Quote                             bool
		AvailableAt                       time.Time
	}{prefixes.registration, prefixDigest(prefixes.records, row.AvailableAt), prefixDigest(prefixes.admissions, row.AvailableAt), row.InputID, row.CaptureKey, row.Quote != nil, row.AvailableAt})
	m.mu.Lock()
	cached, ok := m.entries[key]
	m.mu.Unlock()
	if ok {
		return copySessionEvidence(cached), ctx.Err()
	}
	out, err := inspectOutcomeQuoteSession(ctx, feed, registration, row)
	if err != nil {
		return out, err
	}
	m.mu.Lock()
	m.entries[key] = copySessionEvidence(out)
	m.mu.Unlock()
	return out, nil
}

func copySessionEvidence(e PaperOutcomeSessionEvidence) PaperOutcomeSessionEvidence {
	out := e
	out.PendingCalendarAdmissions = slices.Clone(e.PendingCalendarAdmissions)
	out.CalendarSources = slices.Clone(e.CalendarSources)
	for i := range out.CalendarSources {
		out.CalendarSources[i].Admissions = slices.Clone(e.CalendarSources[i].Admissions)
	}
	return out
}
