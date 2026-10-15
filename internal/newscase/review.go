package newscase

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

// ReviewGroup is built only from application-retained news. It includes exact
// claim excerpts and lineage, never full original articles.
func ReviewGroup(s State, id, receiver string) (news.EventGroup, error) {
	c, ok := s.Cases[id]
	if !ok {
		return news.EventGroup{}, ErrNotFound
	}
	if !valid(s) || c.Behavior != "research" || c.Independence != "not_assessed" || c.ID != CaseID(c.EvidenceClass, c.EventKey) {
		return news.EventGroup{}, ErrInvalid
	}
	group := news.EventGroup{ID: id, EventKey: c.EventKey, EvidenceClass: c.EvidenceClass, RequiresReview: c.Status == "open", Claims: []news.EventClaim{}, Changes: []news.EventChange{}}
	superseded := map[string]bool{}
	// Validate the complete bounded retained chain before selecting excerpts.
	for key, r := range s.Records {
		result, err := news.Normalize(r.Recording)
		if err != nil || key != RecordKey(r.Recording) || !reflect.DeepEqual(result, r.Result) {
			return news.EventGroup{}, ErrInvalid
		}
		if p := r.Recording.Supersedes; p != nil {
			parentKey := RecordKey(news.Recording{SourceID: p.SourceID, ArticleID: p.ArticleID, EvidenceClass: r.Recording.EvidenceClass, Revision: p.Revision})
			parent, ok := s.Records[parentKey]
			if !ok || parent.Result.RecordingSHA256 != p.RecordingSHA256 || parent.Recording.Kind == "retraction" || r.Recording.AvailableAt.Before(parent.Recording.AvailableAt) || r.Recording.ReceivedAt.Before(parent.Recording.ReceivedAt) {
				return news.EventGroup{}, ErrInvalid
			}
			superseded[parentKey] = true
		}
	}
	seen := map[string]bool{}
	if len(c.RecordKeys) != c.Version {
		return news.EventGroup{}, ErrInvalid
	}
	for _, key := range c.RecordKeys {
		if seen[key] {
			return news.EventGroup{}, ErrInvalid
		}
		seen[key] = true
		r, ok := s.Records[key]
		if !ok || r.Recording.EvidenceClass != c.EvidenceClass {
			return news.EventGroup{}, ErrInvalid
		}
		for _, claim := range r.Result.Claims {
			if claim.EventKey != c.EventKey {
				continue
			}
			state := "current"
			if superseded[key] {
				state = "superseded"
			} else if r.Recording.Kind == "retraction" {
				state = "retracted"
			}
			group.Claims = append(group.Claims, news.EventClaim{RecordKey: key, Reference: "news-service:" + receiver + ":record-" + key + ".json", State: state, Claim: claim})
		}
		if p := r.Recording.Supersedes; p != nil {
			group.Changes = append(group.Changes, news.EventChange{RecordKey: key, PreviousRecordKey: RecordKey(news.Recording{SourceID: p.SourceID, ArticleID: p.ArticleID, EvidenceClass: c.EvidenceClass, Revision: p.Revision}), Kind: r.Recording.Kind, AvailableAt: r.Recording.AvailableAt, ReceivedAt: r.Recording.ReceivedAt})
		}
	}
	slices.SortFunc(group.Claims, func(a, b news.EventClaim) int { return strings.Compare(a.Claim.Key, b.Claim.Key) })
	slices.SortFunc(group.Changes, func(a, b news.EventChange) int { return strings.Compare(a.RecordKey, b.RecordKey) })
	raw, _ := json.Marshal(group)
	if len(group.Claims) > 32 || len(raw) > 256<<10 {
		return news.EventGroup{}, ErrCapacity
	}
	return group, nil
}
