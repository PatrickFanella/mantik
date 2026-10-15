// Package newscase admits attributable news into research-only event cases.
// It cannot create decisions, operations or trading effects.
package newscase

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const Schema = "mantik-news-cases-v1"
const Policy = "news-research-reopen-on-evidence-v1"
const MaxCases = 128

var ErrInvalid = errors.New("invalid news admission or case command")
var ErrConflict = errors.New("news identity, predecessor, case version or clock conflict")
var ErrCapacity = errors.New("news research capacity reached")
var ErrNotFound = errors.New("news record or case not found")

type State struct {
	Schema    string            `json:"schema"`
	Policy    string            `json:"policy"`
	LastClock time.Time         `json:"last_clock"`
	Records   map[string]Record `json:"records"`
	Cases     map[string]Case   `json:"cases"`
}
type Case struct {
	ID            string    `json:"id"`
	EventKey      string    `json:"event_key"`
	EvidenceClass string    `json:"evidence_class"`
	Behavior      string    `json:"behavior"`
	Status        string    `json:"status"`
	Generation    int       `json:"generation"`
	Version       int       `json:"version"`
	OpenedAt      time.Time `json:"opened_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	RecordKeys    []string  `json:"record_keys"`
	LastRecordKey string    `json:"last_record_key"`
	Independence  string    `json:"independence"`
}
type Match struct {
	CaseID     string `json:"case_id"`
	Transition string `json:"transition"`
	Version    int    `json:"version"`
}
type Receipt struct {
	RecordKey       string    `json:"record_key"`
	RecordingSHA256 string    `json:"recording_sha256"`
	AcceptedAt      time.Time `json:"accepted_at"`
	Duplicate       bool      `json:"duplicate"`
	Matches         []Match   `json:"matches"`
}
type Record struct {
	Recording news.Recording `json:"recording"`
	Result    news.Result    `json:"result"`
	Receipt   Receipt        `json:"receipt"`
}

func InitialState() State {
	return State{Schema: Schema, Policy: Policy, Records: map[string]Record{}, Cases: map[string]Case{}}
}
func copyOf[T any](value T) T {
	raw, _ := json.Marshal(value)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func valid(s State) bool {
	return s.Schema == Schema && s.Policy == Policy && s.Records != nil && s.Cases != nil && len(s.Records) <= news.MaxRecords && len(s.Cases) <= MaxCases
}
func validClock(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}
func CaseID(class, event string) string {
	raw, _ := json.Marshal([]string{class, event})
	return "news-case-" + news.SHA256(string(raw))
}
func RecordKey(r news.Recording) string {
	raw, _ := json.Marshal([]any{r.SourceID, r.ArticleID, r.EvidenceClass, r.Revision})
	return news.SHA256(string(raw))
}

// Admit atomically retains original bytes, their derived claims and case links.
// The caller supplies the receiver's clock after acquiring its persistence lock.
func Admit(s State, r news.Recording, now time.Time) (State, Receipt, error) {
	if !valid(s) {
		return s, Receipt{}, ErrInvalid
	}
	result, err := news.Normalize(r)
	if err != nil {
		return s, Receipt{}, errors.Join(ErrInvalid, err)
	}
	key := RecordKey(r)
	if previous, ok := s.Records[key]; ok {
		if previous.Result.RecordingSHA256 != result.RecordingSHA256 {
			return s, Receipt{}, ErrConflict
		}
		receipt := copyOf(previous.Receipt)
		receipt.Duplicate = true
		return s, receipt, nil
	}
	if !validClock(now) || now.Before(s.LastClock) || r.ReceivedAt.After(now) {
		return s, Receipt{}, ErrConflict
	}
	if len(s.Records) >= news.MaxRecords {
		return s, Receipt{}, ErrCapacity
	}
	affected := map[string]bool{}
	for _, c := range result.Claims {
		affected[c.EventKey] = true
	}
	if p := r.Supersedes; p != nil {
		parent, ok := s.Records[RecordKey(news.Recording{SourceID: p.SourceID, ArticleID: p.ArticleID, EvidenceClass: r.EvidenceClass, Revision: p.Revision})]
		if !ok || parent.Result.RecordingSHA256 != p.RecordingSHA256 || parent.Recording.Kind == "retraction" || r.AvailableAt.Before(parent.Recording.AvailableAt) || r.ReceivedAt.Before(parent.Recording.ReceivedAt) {
			return s, Receipt{}, ErrConflict
		}
		for _, c := range parent.Result.Claims {
			affected[c.EventKey] = true
		}
	}
	events := []string{}
	newCases := 0
	for event := range affected {
		events = append(events, event)
		if _, ok := s.Cases[CaseID(r.EvidenceClass, event)]; !ok {
			newCases++
		}
	}
	if len(s.Cases)+newCases > MaxCases {
		return s, Receipt{}, ErrCapacity
	}
	slices.Sort(events)
	next := copyOf(s)
	now = now.UTC()
	receipt := Receipt{RecordKey: key, RecordingSHA256: result.RecordingSHA256, AcceptedAt: now, Matches: []Match{}}
	for _, event := range events {
		id := CaseID(r.EvidenceClass, event)
		c, exists := next.Cases[id]
		transition := "updated"
		if !exists {
			c = Case{ID: id, EventKey: event, EvidenceClass: r.EvidenceClass, Behavior: "research", Status: "open", Generation: 1, OpenedAt: now, RecordKeys: []string{}, Independence: "not_assessed"}
			transition = "opened"
		} else if c.Status == "closed" {
			c.Status = "open"
			c.Generation++
			c.OpenedAt = now
			transition = "reopened"
		}
		c.Version++
		c.UpdatedAt = now
		c.LastRecordKey = key
		c.RecordKeys = append(c.RecordKeys, key)
		next.Cases[id] = c
		receipt.Matches = append(receipt.Matches, Match{CaseID: id, Transition: transition, Version: c.Version})
	}
	r.PublishedAt, r.AvailableAt, r.ReceivedAt = r.PublishedAt.UTC(), r.AvailableAt.UTC(), r.ReceivedAt.UTC()
	next.Records[key] = copyOf(Record{Recording: r, Result: result, Receipt: receipt})
	next.LastClock = now
	return next, copyOf(receipt), nil
}

// Version counts admitted evidence, not operator closures. Repeating a close
// for that same version is harmless; new evidence always increments the fence.
func Close(s State, id string, version int, now time.Time) (State, Case, error) {
	if !valid(s) || version < 1 {
		return s, Case{}, ErrInvalid
	}
	c, ok := s.Cases[id]
	if !ok {
		return s, Case{}, ErrNotFound
	}
	if c.Version != version {
		return s, Case{}, ErrConflict
	}
	if c.Status == "closed" {
		return s, copyOf(c), nil
	}
	if !validClock(now) || now.Before(s.LastClock) {
		return s, Case{}, ErrConflict
	}
	next := copyOf(s)
	c = next.Cases[id]
	c.Status = "closed"
	c.UpdatedAt = now.UTC()
	next.Cases[id] = c
	next.LastClock = now.UTC()
	return next, copyOf(c), nil
}
