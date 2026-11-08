package storage

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

// windowFeed is one registration with a calendar and a run of later quotes,
// each published a second after its receipt.
func windowFeed(at time.Time, quotes int) FeedEvidenceSnapshot {
	feed := FeedEvidenceSnapshot{ReceiverID: "receiver", Collectors: feedcollector.InitialState(), Routing: &domain.RoutingState{Inputs: map[string]domain.RetainedRoutingInput{}}, Records: map[string]FeedEvidenceRecord{}}
	add := func(id, capability string, received time.Time) {
		raw, _ := json.Marshal(map[string]any{"capability": capability, "received_at": received})
		feed.Records["reg:"+id] = FeedEvidenceRecord{RegistrationID: "reg", Key: id, PublishedAt: received.Add(time.Second), Recording: raw}
		feed.Collectors.Admissions[id] = feedcollector.Admission{Fence: feedcollector.Fence{RegistrationID: "reg"}, At: received}
		feed.Routing.Inputs[id] = domain.RetainedRoutingInput{Input: domain.RoutingInput{ID: id, ReceivedAt: received}}
	}
	add("calendar", "market_calendar", at)
	for n := range quotes {
		add(fmt.Sprintf("quote-%02d", n), "latest_quotes", at.Add(time.Duration(n+1)*time.Minute))
	}
	return feed
}

func TestFeedWindowKeepsTheEvaluatedEdge(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	feed := windowFeed(at, 6)
	index := newFeedPrefixIndex(feed)
	cut := at.Add(10 * time.Minute)
	// The calendar is the older latest, so the window runs from it.
	records, admissions := index.window("reg", cut, time.Time{})
	if len(records) != 7 || len(admissions) != 7 {
		t.Fatal(len(records), len(admissions))
	}
	// A newer calendar narrows the window to the quotes since it.
	raw, _ := json.Marshal(map[string]any{"capability": "market_calendar", "received_at": at.Add(5 * time.Minute)})
	feed.Records["reg:calendar-new"] = FeedEvidenceRecord{RegistrationID: "reg", Key: "calendar-new", PublishedAt: at.Add(5*time.Minute + time.Second), Recording: raw}
	feed.Collectors.Admissions["calendar-new"] = feedcollector.Admission{Fence: feedcollector.Fence{RegistrationID: "reg"}, At: at.Add(5 * time.Minute)}
	feed.Routing.Inputs["calendar-new"] = domain.RetainedRoutingInput{Input: domain.RoutingInput{ID: "calendar-new", ReceivedAt: at.Add(5 * time.Minute)}}
	records, admissions = newFeedPrefixIndex(feed).window("reg", cut, time.Time{})
	if len(records) != 3 || !records["reg:calendar-new"] || !records["reg:quote-05"] || !records["reg:quote-04"] || records["reg:quote-03"] || !admissions["quote-04"] || admissions["quote-03"] {
		t.Fatal(records, admissions)
	}
	// An outcome keeps everything from its decision's clock onward.
	records, _ = newFeedPrefixIndex(feed).window("reg", cut, at.Add(6*time.Minute))
	if len(records) != 3 || !records["reg:calendar-new"] || !records["reg:quote-04"] || !records["reg:quote-05"] {
		t.Fatal(records)
	}
}

// Digests cover the whole prefix, so anything added, removed or changed at or
// before the cut shows, while later records do not disturb it.
func TestFeedPrefixBindsTheWholeHistory(t *testing.T) {
	at := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	cut := at.Add(3*time.Minute + time.Second)
	feed := windowFeed(at, 6)
	prefix := newFeedPrefixIndex(feed).at("reg", cut)
	if prefix.Records != 4 || prefix.Admissions != 4 || prefix.RecordsSHA256 == "" || prefix.AdmissionsSHA256 == "" {
		t.Fatal(prefix)
	}
	removed := windowFeed(at, 6)
	delete(removed.Records, "reg:quote-00")
	if newFeedPrefixIndex(removed).at("reg", cut) == prefix {
		t.Fatal("removed record not detected")
	}
	changed := windowFeed(at, 6)
	record := changed.Records["reg:quote-00"]
	record.Recording = json.RawMessage(`{"capability":"latest_quotes","received_at":"2026-11-09T15:01:00Z","changed":true}`)
	changed.Records["reg:quote-00"] = record
	if newFeedPrefixIndex(changed).at("reg", cut) == prefix {
		t.Fatal("changed record not detected")
	}
	admission := windowFeed(at, 6)
	delete(admission.Collectors.Admissions, "quote-00")
	if newFeedPrefixIndex(admission).at("reg", cut) == prefix {
		t.Fatal("removed admission not detected")
	}
	if later := windowFeed(at, 9); newFeedPrefixIndex(later).at("reg", cut) != prefix {
		t.Fatal("later records changed an earlier prefix")
	}
	// A store that reorders JSON keys does not change the digest.
	reordered := windowFeed(at, 6)
	for key, r := range reordered.Records {
		var value map[string]any
		_ = json.Unmarshal(r.Recording, &value)
		r.Recording, _ = json.Marshal(map[string]any{"received_at": value["received_at"], "capability": value["capability"]})
		reordered.Records[key] = r
	}
	if newFeedPrefixIndex(reordered).at("reg", cut) != prefix {
		t.Fatal("key order changed the digest")
	}
}
