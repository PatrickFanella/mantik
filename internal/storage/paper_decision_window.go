package storage

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const (
	paperDecisionSchemaV3 = "mantik-owned-paper-decision-v3"
	paperOutcomeSchemaV3  = "mantik-owned-paper-outcome-v3"
	paperDecisionSchemaV4 = "mantik-owned-paper-decision-v4"
	paperOutcomeSchemaV4  = "mantik-owned-paper-outcome-v4"
)

// PaperDecisionFeedPrefix binds everything a registration had retained at a
// cut without copying it: a count and a chained digest over its publications
// in (published_at, key) order, and the same over its admissions with their
// routed inputs in (at, id) order.
type PaperDecisionFeedPrefix struct {
	Records          int    `json:"records"`
	RecordsSHA256    string `json:"records_sha256"`
	Admissions       int    `json:"admissions"`
	AdmissionsSHA256 string `json:"admissions_sha256"`
}

// window selects what an evaluation can read from a registration's retained
// feed at a cut. Coverage reads the latest quote and calendar by receipt
// clock, ties included, and an admission matters only if it is at least as new
// as one of those, so the window starts at the older of the two. An outcome
// also reads everything published from since, its decision's clock, onward.
// Everything before the window stays bound through the prefix digests instead
// of being copied. With an unreadable recording the window keeps everything.
func (x *feedPrefixIndex) window(registration string, cut, since time.Time) (map[string]bool, map[string]bool) {
	c := x.chains(registration)
	latest := map[string]time.Time{}
	var start time.Time
	lower := func(at time.Time) {
		if start.IsZero() || at.Before(start) {
			start = at
		}
	}
	whole := false
	forward := func(r feedRecordClock) bool { return !since.IsZero() && !r.publishedAt.Before(since) }
	for _, r := range c.clocks {
		if r.publishedAt.After(cut) {
			continue
		}
		if !r.readable {
			whole = true
			break
		}
		if forward(r) {
			lower(r.receivedAt)
		} else if r.receivedAt.After(latest[r.capability]) {
			latest[r.capability] = r.receivedAt
		}
	}
	for _, at := range latest {
		lower(at)
	}
	records, admissions := map[string]bool{}, map[string]bool{}
	for _, r := range c.clocks {
		if !r.publishedAt.After(cut) && (whole || start.IsZero() || forward(r) || !r.receivedAt.Before(start)) {
			records[r.key] = true
		}
	}
	for _, a := range c.admissionClocks {
		if !a.at.After(cut) && (whole || start.IsZero() || !a.receivedAt.Before(start)) {
			admissions[a.id] = true
		}
	}
	return records, admissions
}

type feedPrefixPoint struct {
	at     time.Time
	digest string
}

// feedPrefixIndex answers prefix digests for one feed snapshot, building each
// registration's chain once however many retained bases ask.
type feedPrefixIndex struct {
	feed  FeedEvidenceSnapshot
	built map[string]*registrationPrefixes
}

type registrationPrefixes struct {
	records, admissions []feedPrefixPoint
	clocks              []feedRecordClock
	admissionClocks     []feedAdmissionClock
	seed                PaperDecisionFeedPrefix
	seedAt              time.Time
	seedSourceAt        time.Time
}

type feedRecordClock struct {
	key, capability string
	receivedAt      time.Time
	publishedAt     time.Time
	readable        bool
}

type feedAdmissionClock struct {
	id             string
	at, receivedAt time.Time
}

func newFeedPrefixIndex(feed FeedEvidenceSnapshot) *feedPrefixIndex {
	return &feedPrefixIndex{feed: feed, built: map[string]*registrationPrefixes{}}
}

func (x *feedPrefixIndex) chains(registration string) *registrationPrefixes {
	if out, ok := x.built[registration]; ok {
		return out
	}
	out := &registrationPrefixes{}
	if r := x.feed.Retention; r != nil {
		out.seed, out.seedAt, out.seedSourceAt = r.prefix(registration), r.Checkpoint.CutAt, r.Checkpoint.Archive.ArchivedAt
	}
	keys := []string{}
	for key, r := range x.feed.Records {
		if r.RegistrationID == registration {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		if c := x.feed.Records[a].PublishedAt.Compare(x.feed.Records[b].PublishedAt); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	previous := out.seed.RecordsSHA256
	for _, key := range keys {
		r := x.feed.Records[key]
		clock := feedRecordClock{key: key, publishedAt: r.PublishedAt}
		var parsed struct {
			Capability string    `json:"capability"`
			ReceivedAt time.Time `json:"received_at"`
		}
		if json.Unmarshal(r.Recording, &parsed) == nil && !parsed.ReceivedAt.IsZero() {
			clock.capability, clock.receivedAt, clock.readable = parsed.Capability, parsed.ReceivedAt, true
		}
		out.clocks = append(out.clocks, clock)
		if !out.seedAt.IsZero() && !r.PublishedAt.After(out.seedAt) {
			continue
		}
		previous = publicationPrefixDigest(previous, key, r)
		out.records = append(out.records, feedPrefixPoint{r.PublishedAt, previous})
	}
	ids := []string{}
	for id, a := range x.feed.Collectors.Admissions {
		if a.Fence.RegistrationID == registration {
			ids = append(ids, id)
		}
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := x.feed.Collectors.Admissions[a].At.Compare(x.feed.Collectors.Admissions[b].At); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	previous = out.seed.AdmissionsSHA256
	for _, id := range ids {
		var input domain.RetainedRoutingInput
		if x.feed.Routing != nil {
			input = x.feed.Routing.Inputs[id]
		}
		a := x.feed.Collectors.Admissions[id]
		out.admissionClocks = append(out.admissionClocks, feedAdmissionClock{id, a.At, input.Input.ReceivedAt})
		if !out.seedAt.IsZero() && !a.At.After(out.seedAt) {
			continue
		}
		previous = admissionPrefixDigest(previous, id, a, input)
		out.admissions = append(out.admissions, feedPrefixPoint{a.At, previous})
	}
	x.built[registration] = out
	return out
}

func prefixAt(points []feedPrefixPoint, cut time.Time) (int, string) {
	n, _ := slices.BinarySearchFunc(points, cut, func(p feedPrefixPoint, cut time.Time) int {
		if p.at.After(cut) {
			return 1
		}
		return -1
	})
	if n == 0 {
		return 0, ""
	}
	return n, points[n-1].digest
}

func (x *feedPrefixIndex) at(registration string, cut time.Time) PaperDecisionFeedPrefix {
	c := x.chains(registration)
	records, recordsDigest := prefixAt(c.records, cut)
	admissions, admissionsDigest := prefixAt(c.admissions, cut)
	if !c.seedAt.IsZero() {
		// The checkpoint also closes the source-free interval between its named
		// archive time and its possibly later paper cut.
		if cut.Before(c.seedSourceAt) {
			return PaperDecisionFeedPrefix{}
		}
		if records == 0 {
			recordsDigest = c.seed.RecordsSHA256
		}
		if admissions == 0 {
			admissionsDigest = c.seed.AdmissionsSHA256
		}
		records += c.seed.Records
		admissions += c.seed.Admissions
	}
	return PaperDecisionFeedPrefix{records, recordsDigest, admissions, admissionsDigest}
}

// verifyFeedPrefix checks a windowed basis against everything the current
// feed retains for its registration at the cut: the digests over the whole
// prefix, and that the basis holds exactly the window that prefix selects. It
// replaces the legacy per-basis membership scan.
func verifyFeedPrefix(c PaperDecisionCommand, b PaperDecisionBasis, at, since time.Time, index *feedPrefixIndex) error {
	if b.Prefix == nil || *b.Prefix != index.at(c.RegistrationID, at) {
		return ErrPaperDecisionConflict
	}
	records, admissions := index.window(c.RegistrationID, at, since)
	if len(records) != len(b.Feed.Records) || len(admissions) != len(b.Feed.Collectors.Admissions) {
		return ErrPaperDecisionConflict
	}
	for key := range b.Feed.Records {
		if !records[key] {
			return ErrPaperDecisionConflict
		}
	}
	for id := range b.Feed.Collectors.Admissions {
		if !admissions[id] {
			return ErrPaperDecisionConflict
		}
	}
	return nil
}

// withFeedPrefix returns the windowed form of a full basis, bound at its cut.
func withFeedPrefix(c PaperDecisionCommand, full PaperDecisionBasis, feed FeedEvidenceSnapshot, at, since time.Time) PaperDecisionBasis {
	index := newFeedPrefixIndex(feed)
	records, admissions := index.window(c.RegistrationID, at, since)
	out := full
	out.Feed.Records = map[string]FeedEvidenceRecord{}
	for key := range records {
		out.Feed.Records[key] = full.Feed.Records[key]
	}
	out.Feed.Collectors.Admissions = map[string]feedcollector.Admission{}
	out.Feed.Routing = &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{}}
	for id := range admissions {
		out.Feed.Collectors.Admissions[id] = full.Feed.Collectors.Admissions[id]
		out.Feed.Routing.Inputs[id] = full.Feed.Routing.Inputs[id]
	}
	prefix := index.at(c.RegistrationID, at)
	out.Prefix = &prefix
	return out
}

func publicationPrefixDigest(previous, key string, r FeedEvidenceRecord) string {
	recording, _ := canonicalRecording(r.Recording)
	r.Recording = nil
	return workerplan.Hash(struct {
		Previous, Key, Recording string
		Record                   FeedEvidenceRecord
	}{previous, key, recording, r})
}
func admissionPrefixDigest(previous, id string, a feedcollector.Admission, input domain.RetainedRoutingInput) string {
	return workerplan.Hash(struct {
		Previous, ID string
		Admission    feedcollector.Admission
		Input        domain.RetainedRoutingInput
	}{previous, id, a, input})
}

// V4 archive-aware windows use the same prefix and selection rules as v3,
// with canonical recording metadata independent of live/archive JSON ordering.
// Provider original strings remain byte-for-byte source material.
func canonicalPaperBasis(b PaperDecisionBasis) (PaperDecisionBasis, error) {
	b = clone(b)
	for key, record := range b.Feed.Records {
		raw, err := canonicalRecording(record.Recording)
		if err != nil {
			return PaperDecisionBasis{}, ErrPaperDecisionInvalid
		}
		record.Recording = json.RawMessage(raw)
		b.Feed.Records[key] = record
	}
	return b, nil
}
func canonicalPaperBasisValid(b PaperDecisionBasis) bool {
	if b.Prefix == nil {
		return false
	}
	for _, record := range b.Feed.Records {
		raw, err := canonicalRecording(record.Recording)
		if err != nil || raw != string(record.Recording) {
			return false
		}
	}
	return true
}
