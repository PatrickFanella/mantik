package storage

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Storage's publication identity lookup must match the provider normalizer,
// including instruments whose symbols share a prefix and empty calendars.
func TestPublishedFeedInputIDsMatchNormalizer(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	for _, capability := range []string{"latest_quotes", "market_calendar", "empty_calendar"} {
		t.Run(capability, func(t *testing.T) {
			r := alpaca.Recording{Schema: alpaca.RecordingSchema, Currency: "USD", CaptureID: "identity", Revision: 3, Capability: "latest_quotes", Feed: "iex", Symbols: []string{"BRK-B", "BRK"}, ReceivedAt: at, EvidenceClass: "prospective_market_unqualified", AccessRef: "fixture-only", RawRef: "journal:identity:capture-identity-r3.json", MaxQuoteAgeSeconds: 60, Original: `{"quotes":{}}`}
			if capability != "latest_quotes" {
				r.Capability, r.Feed = "market_calendar", "regular_session"
				r.Start, r.End = "2026-11-09", "2026-11-09"
				r.Original = `[{"date":"2026-11-09","open":"09:30","close":"16:00"}]`
				if capability == "empty_calendar" {
					r.Original = `[]`
				}
			}
			r.SHA256 = alpaca.Digest(r.Original)
			normalized, err := alpaca.Normalize(r)
			if err != nil {
				t.Fatal(err)
			}
			expected := []string{}
			for _, input := range normalized.Inputs {
				expected = append(expected, input.ID)
			}
			raw, _ := json.Marshal(r)
			actual, err := publishedFeedInputIDs(raw)
			slices.Sort(actual)
			slices.Sort(expected)
			if err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("retirement publication identity differs from actual normalizer", actual, expected, err)
			}
		})
	}
}

func TestFeedRetentionWorkingSetAndPrefixContinuation(t *testing.T) {
	m := NewMemory()
	base := time.Date(2026, 11, 9, 15, 0, 0, 100, time.UTC)
	feed := windowFeed(base, 6)
	feed.ReceiverID = m.receiverID
	entry := feedcollector.Entry{Registration: feedcollector.Registration{ID: "reg"}, RegisteredAt: base, Lease: &feedcollector.Lease{OwnerID: "worker", Generation: 1, ClaimedAt: base, UpdatedAt: base.Add(time.Minute), ExpiresAt: base.Add(2 * time.Minute)}}
	feed.Collectors.Entries["reg"] = entry
	feed.Collectors.LastClock = base.Add(6*time.Minute + time.Second)
	feed.Collectors.Commands["latest"] = feedcollector.RetainedCommand{Command: feedcollector.Command{RequestID: "latest", Operation: "renew", RegistrationID: "reg"}, Receipt: feedcollector.Receipt{RequestID: "latest", Operation: "renew", At: entry.Lease.UpdatedAt, Entry: entry}}
	feed.Collectors.Commands["old"] = feedcollector.RetainedCommand{Command: feedcollector.Command{RequestID: "old", Operation: "claim", RegistrationID: "reg"}, Receipt: feedcollector.Receipt{RequestID: "old", Operation: "claim", At: base, Entry: feedcollector.Entry{Registration: entry.Registration, RegisteredAt: base}}}
	add := func(f *FeedEvidenceSnapshot, id, capability string, at time.Time) {
		raw, _ := json.Marshal(map[string]any{"capability": capability, "received_at": at})
		f.Records["reg:"+id] = FeedEvidenceRecord{RegistrationID: "reg", Key: id, PublishedAt: at.Add(time.Second), Recording: raw}
		f.Collectors.Admissions[id] = feedcollector.Admission{Fence: feedcollector.Fence{RegistrationID: "reg"}, At: at}
		f.Routing.Inputs[id] = domain.RetainedRoutingInput{Input: domain.RoutingInput{ID: id, ReceivedAt: at}}
		f.Collectors.LastClock = at.Add(time.Second)
	}
	add(&feed, "calendar-new", "market_calendar", base.Add(5*time.Minute))
	feed.Collectors.LastClock = base.Add(6*time.Minute + time.Second)
	feed.Routing.Runtime = map[string]domain.TriggerRuntime{"live": {Facts: map[string]domain.ConditionFact{"price": {InputRef: "quote-00"}}}}
	feed.Routing.Cases = []domain.RoutingCase{{ID: "case", LastInputRef: "quote-01"}}
	feed.Routing.Inputs["other-owner"] = domain.RetainedRoutingInput{Input: domain.RoutingInput{ID: "other-owner", ReceivedAt: base}}
	before := clone(feed)
	cut := base.Add(10 * time.Minute)
	canonical, err := canonicalArchiveSource(feed)
	if err != nil {
		t.Fatal(err)
	}
	check := func(FeedEvidenceSnapshot, time.Time) error { return nil }
	a, err := newFeedArchive(canonical, FeedArchiveHeader{}, cut, check)
	if err != nil {
		t.Fatal(err)
	}
	strategies, err := m.PaperStrategies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	e := &paperFeedArchiveEvaluators{decision: func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Fatal("empty paper owner evaluated")
		return PaperDecisionEvaluation{}, nil
	}, outcome: func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		t.Fatal("empty paper owner evaluated")
		return PaperOutcomeEvaluation{}, nil
	}}
	data, err := paperFeedCheckpointData(a, nil, nil, strategies, e)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := finishPaperFeedCheckpoint(data, PaperFeedCheckpointHeader{})
	if err != nil {
		t.Fatal(err)
	}
	pruned, retirement, err := retainedFeedSources(feed, cp, nil)
	if err != nil || retirement.Validate() != nil || !reflect.DeepEqual(feed, before) {
		t.Fatal("retirement changed source or lost custody", err)
	}
	if len(pruned.Records) != 3 || len(pruned.Collectors.Admissions) != 8 || len(pruned.Collectors.Commands) != 1 || len(pruned.Routing.Inputs) != 9 || !reflect.DeepEqual(pruned.Collectors.Entries, feed.Collectors.Entries) || !reflect.DeepEqual(pruned.Routing.Runtime, feed.Routing.Runtime) || !reflect.DeepEqual(pruned.Routing.Cases, feed.Routing.Cases) {
		t.Fatal("working set lost a live reference", pruned)
	}
	if _, ok := pruned.Collectors.Commands["latest"]; !ok {
		t.Fatal("current lease proof retired")
	}
	want := newFeedPrefixIndex(feed).at("reg", cut)
	if got := newFeedPrefixIndex(pruned).at("reg", cut); got != want {
		t.Fatal("retirement changed complete prefix", got, want)
	}
	b, err := newFeedArchive(clone(pruned), a.FeedArchiveHeader, cut.Add(time.Minute), check)
	if err != nil || b.Schema != "mantik-feed-archive-v2" || extendsFeedArchive(a, b.Source) != nil {
		t.Fatal("approved bounded successor refused", err)
	}
	points, err := archivePrefixPoints(b)
	if err != nil || len(points) != 0 {
		t.Fatal("retained sources were counted twice", len(points), err)
	}
	full := clone(feed)
	add(&full, "tail", "latest_quotes", cut.Add(2*time.Minute))
	add(&pruned, "tail", "latest_quotes", cut.Add(2*time.Minute))
	when := cut.Add(2*time.Minute + time.Second)
	if got, want := newFeedPrefixIndex(pruned).at("reg", when), newFeedPrefixIndex(full).at("reg", when); got != want {
		t.Fatal("new tail did not extend original digest", got, want)
	}
	c, err := newFeedArchive(clone(pruned), b.FeedArchiveHeader, when, check)
	if err != nil || extendsFeedArchive(b, c.Source) != nil {
		t.Fatal("next bounded tail refused", err)
	}
	points, err = archivePrefixPoints(c)
	if err != nil || len(points) != 2 || points[0].Count != want.Records+1 || points[1].Count != want.Admissions+1 || points[0].PreviousSHA256 != want.RecordsSHA256 || points[1].PreviousSHA256 != want.AdmissionsSHA256 {
		t.Fatal("archive lost original prefix seed", points, err)
	}
	for _, arch := range []FeedArchive{a, b, c} {
		additions, err := m.prepareArchivePrefixes(arch)
		if err != nil {
			t.Fatal("prefix append", arch.Sequence, err)
		}
		if m.feedArchivePrefixPoints == nil {
			m.feedArchivePrefixPoints = map[string]feedArchivePrefixRef{}
			m.feedArchivePrefixOrder = map[string][]feedArchivePrefixRef{}
		}
		for _, ref := range additions {
			m.feedArchivePrefixPoints[prefixPointKey(ref.Point)] = ref
			group := prefixGroup(ref.Point)
			m.feedArchivePrefixOrder[group] = append(m.feedArchivePrefixOrder[group], ref)
		}
		m.feedArchives = append(m.feedArchives, arch)
		m.feedArchiveRecords, err = indexFeedArchive(m.feedArchiveRecords, arch)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{cut, cut.Add(30 * time.Second), b.ArchivedAt, c.ArchivedAt} {
		p, err := m.FeedArchivePrefix(t.Context(), "reg", at)
		if err != nil || p.Prefix != newFeedPrefixIndex(full).at("reg", at) {
			t.Fatal("archived seeded cut changed", at, p, err)
		}
	}
	if _, err := m.AuditFeedArchiveSources(t.Context()); err != nil {
		t.Fatal("source audit rejected valid retirement chain", err)
	}
	bad := clone(pruned)
	delete(bad.Records, "reg:quote-05")
	if validateRetainedFeed(bad) == nil {
		t.Fatal("missing kept source became intentional retirement")
	}
	bad = clone(pruned)
	bad.Records["reg:backfill"] = FeedEvidenceRecord{RegistrationID: "reg", Key: "backfill", PublishedAt: base, Recording: json.RawMessage(`{}`)}
	if validateRetainedFeed(bad) == nil {
		t.Fatal("retired prefix admitted a backfill")
	}
	forged := clone(b.Source)
	forged.Retention.Prefixes[0].Prefix.Records--
	forged.Retention.Checkpoint.PrefixesSHA256 = workerplan.Hash(forged.Retention.Prefixes)
	forged.Retention.Checkpoint.SHA256 = ""
	forged.Retention.Checkpoint.SHA256 = workerplan.Hash(forged.Retention.Checkpoint)
	forged.Retention.SHA256 = ""
	forged.Retention.SHA256 = workerplan.Hash(*forged.Retention)
	if forged.Retention.Validate() != nil {
		t.Fatal("forged fixture is not self-consistent")
	}
	if !errors.Is(extendsFeedArchive(a, forged), ErrFeedArchiveConflict) {
		t.Fatal("self-rehashed prefix replaced original source")
	}
}
