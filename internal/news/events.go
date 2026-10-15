package news

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"
)

const EventSchema = "mantik-news-events-v1"
const EventPolicy = "declared-event-revision-review-v1"

// EventGroups is a derived research view, not a service case or review ledger.
type EventGroups struct {
	Schema       string       `json:"schema"`
	Policy       string       `json:"policy"`
	JournalID    string       `json:"journal_id"`
	AsOf         time.Time    `json:"as_of"`
	Independence string       `json:"independence"`
	Groups       []EventGroup `json:"groups"`
}
type EventGroup struct {
	ID             string        `json:"id"`
	EventKey       string        `json:"event_key"`
	EvidenceClass  string        `json:"evidence_class"`
	RequiresReview bool          `json:"requires_review"`
	Claims         []EventClaim  `json:"claims"`
	Changes        []EventChange `json:"changes"`
}
type EventClaim struct {
	RecordKey string     `json:"record_key"`
	Reference string     `json:"reference"`
	State     string     `json:"state"`
	Claim     BoundClaim `json:"claim"`
}
type EventChange struct {
	RecordKey         string    `json:"record_key"`
	PreviousRecordKey string    `json:"previous_record_key"`
	Kind              string    `json:"kind"`
	AvailableAt       time.Time `json:"available_at"`
	ReceivedAt        time.Time `json:"received_at"`
}

func eventID(journal, class, event string) string {
	raw, _ := json.Marshal([]string{journal, class, event})
	return "news-event-" + SHA256(string(raw))
}

// Events includes only revisions received by the explicit cutoff. Full journal
// integrity is still checked, including later records. No persisted state changes.
func (j *Journal) Events(asOf time.Time) (EventGroups, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return EventGroups{}, os.ErrClosed
	}
	if !validTime(asOf) {
		return EventGroups{}, ErrNewsCorrupt
	}
	status, err := j.audit()
	if err != nil {
		return EventGroups{}, err
	}
	out := EventGroups{Schema: EventSchema, Policy: EventPolicy, JournalID: j.ID, AsOf: asOf.UTC(), Independence: "not_assessed", Groups: []EventGroup{}}
	records := map[string]Retained{}
	superseded := map[string]bool{}
	for _, s := range status.Records {
		if s.ReceivedAt.After(asOf) {
			continue
		}
		stored, err := j.readRecord(s.Key)
		if err != nil {
			return EventGroups{}, err
		}
		records[s.Key] = stored
		if p := stored.Recording.Supersedes; p != nil {
			superseded[recordKey(p.SourceID, p.ArticleID, s.EvidenceClass, p.Revision)] = true
		}
	}
	groups := map[string]*EventGroup{}
	group := func(class, event string) *EventGroup {
		id := eventID(j.ID, class, event)
		if groups[id] == nil {
			groups[id] = &EventGroup{ID: id, EventKey: event, EvidenceClass: class, Claims: []EventClaim{}, Changes: []EventChange{}}
		}
		return groups[id]
	}
	for key, stored := range records {
		r := stored.Recording
		affected := map[string]bool{}
		for _, c := range stored.Result.Claims {
			state := "current"
			if superseded[key] {
				state = "superseded"
			} else if r.Kind == "retraction" {
				state = "retracted"
			}
			g := group(r.EvidenceClass, c.EventKey)
			g.Claims = append(g.Claims, EventClaim{RecordKey: key, Reference: "news-journal:" + j.ID + ":record-" + key + ".json", State: state, Claim: c})
			affected[c.EventKey] = true
		}
		if p := r.Supersedes; p != nil {
			previousKey := recordKey(p.SourceID, p.ArticleID, r.EvidenceClass, p.Revision)
			previous, ok := records[previousKey]
			// Causal lineage guarantees the parent is visible at this cutoff.
			if !ok {
				return EventGroups{}, ErrNewsLineage
			}
			for _, c := range previous.Result.Claims {
				affected[c.EventKey] = true
			}
			for event := range affected {
				g := group(r.EvidenceClass, event)
				g.RequiresReview = true
				g.Changes = append(g.Changes, EventChange{RecordKey: key, PreviousRecordKey: previousKey, Kind: r.Kind, AvailableAt: r.AvailableAt, ReceivedAt: r.ReceivedAt})
			}
		}
	}
	for _, g := range groups {
		slices.SortFunc(g.Claims, func(a, b EventClaim) int { return strings.Compare(a.Claim.Key, b.Claim.Key) })
		slices.SortFunc(g.Changes, func(a, b EventChange) int { return strings.Compare(a.RecordKey, b.RecordKey) })
		out.Groups = append(out.Groups, *g)
	}
	slices.SortFunc(out.Groups, func(a, b EventGroup) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}
