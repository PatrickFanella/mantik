// Package review binds recorded-model attempts to application news-case evidence.
// Completion retains research results without closing cases or granting authority.
package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
)

const Schema = "mantik-news-review-attempt-v1"
const MaxAttempts = 32

var ErrInvalid = errors.New("invalid recorded review command")
var ErrConflict = errors.New("review identity, evidence, clock or completion conflict")
var ErrCapacity = errors.New("retained review capacity reached")
var ErrNotFound = errors.New("review attempt not found")
var ID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type Attempt struct {
	Profile             *investigation.Profile `json:"profile,omitempty"`
	HypothesisInventory *question.Inventory    `json:"hypothesis_inventory,omitempty"`
	ModelMode           string                 `json:"model_mode"`
	Schema              string                 `json:"schema"`
	ID                  string                 `json:"id"`
	Status              string                 `json:"status"`
	StartedAt           time.Time              `json:"started_at"`
	Deadline            time.Time              `json:"deadline"`
	CompletedAt         time.Time              `json:"completed_at,omitzero"`
	Pin                 investigation.Pin      `json:"pin"`
	StepsSHA256         string                 `json:"steps_sha256,omitempty"`
	Steps               []string               `json:"steps,omitempty"`
	Result              *investigation.Result  `json:"result,omitempty"`
}
type View struct {
	Attempt          Attempt `json:"attempt"`
	EvidenceCurrent  bool    `json:"evidence_current"`
	ReviewApplicable bool    `json:"review_applicable"`
}

func validTime(t time.Time) bool { return !t.IsZero() && t.UTC().Year() > 0 && t.UTC().Year() <= 9999 }
func Start(s newscase.State, receiver, id, caseID string, version int, now time.Time) (Attempt, error) {
	return StartProfile(s, receiver, id, caseID, version, investigation.TypeID, now)
}
func StartProfile(s newscase.State, receiver, id, caseID string, version int, typeID string, now time.Time) (Attempt, error) {
	return StartProfileInventory(s, receiver, id, caseID, version, typeID, nil, now)
}
func StartProfileInventory(s newscase.State, receiver, id, caseID string, version int, typeID string, inventory *question.Inventory, now time.Time) (Attempt, error) {
	profile, err := investigation.ProfileFor(typeID)
	if err != nil {
		return Attempt{}, ErrInvalid
	}

	if typeID == investigation.ReuseTypeID {
		if inventory == nil || inventory.Validate() != nil || inventory.ReceiverID != receiver {
			return Attempt{}, ErrInvalid
		}
		raw, _ := json.Marshal(inventory)
		var copy question.Inventory
		_ = json.Unmarshal(raw, &copy)
		inventory = &copy
	} else if inventory != nil {
		return Attempt{}, ErrInvalid
	}
	if !ID.MatchString(id) || version < 1 || !validTime(now) || now.Before(s.LastClock) {
		return Attempt{}, ErrInvalid
	}
	c, ok := s.Cases[caseID]
	if !ok {
		return Attempt{}, newscase.ErrNotFound
	}
	if c.Status != "open" || c.Version != version {
		return Attempt{}, ErrConflict
	}
	group, err := newscase.ReviewGroup(s, caseID, receiver)
	if err != nil {
		return Attempt{}, err
	}
	raw, _ := json.Marshal(group)
	subject := investigation.Subject{ReceiverID: receiver, CaseID: c.ID, CaseVersion: c.Version, CaseGeneration: c.Generation, EventGroupID: group.ID, EventKey: group.EventKey, EvidenceClass: group.EvidenceClass, AsOf: now.UTC(), SnapshotSHA256: news.SHA256(string(raw))}
	return Attempt{Profile: &profile, HypothesisInventory: inventory, ModelMode: "recorded_model_fixture", Schema: Schema, ID: id, Status: "pending", StartedAt: now.UTC(), Deadline: now.UTC().Add(time.Duration(profile.Contract.TimeoutSeconds) * time.Second), Pin: investigation.Pin{Subject: subject, Group: group}}, nil
}
func SameStart(a Attempt, receiver, caseID string, version int) bool {
	return SameStartProfile(a, receiver, caseID, version, investigation.TypeID)
}
func SameStartProfile(a Attempt, receiver, caseID string, version int, typeID string) bool {
	c, err := Definition(a)
	if err != nil || c.TypeID != typeID {
		return false
	}
	return a.Schema == Schema && a.Pin.Subject.ReceiverID == receiver && a.Pin.Subject.CaseID == caseID && a.Pin.Subject.CaseVersion == version
}
func Inspect(a Attempt, s newscase.State) View {
	c, ok := s.Cases[a.Pin.Subject.CaseID]
	current := ok && c.Version == a.Pin.Subject.CaseVersion && c.Generation == a.Pin.Subject.CaseGeneration && c.Status == "open"
	return View{Attempt: a, EvidenceCurrent: current, ReviewApplicable: current && a.Status == "completed" && a.Result != nil && ResultMatchesProfile(a, *a.Result) && a.Result.Authority == "research_only_unqualified" && !a.Result.QualificationCredit}
}
func CanonicalSteps(steps []json.RawMessage) ([]json.RawMessage, string, error) {
	if len(steps) < 1 || len(steps) > 16 {
		return nil, "", ErrInvalid
	}
	raw, err := json.Marshal(steps)
	if err != nil || len(raw) > 128<<10 {
		return nil, "", ErrInvalid
	}
	var copy []json.RawMessage
	if err = json.Unmarshal(raw, &copy); err != nil {
		return nil, "", ErrInvalid
	}
	return copy, news.SHA256(string(raw)), nil
}

type fixtureModel struct {
	steps []json.RawMessage
	next  int
}

func (m *fixtureModel) Next(ctx context.Context, _ investigation.Frame) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.next >= len(m.steps) {
		return nil, io.EOF
	}
	r := m.steps[m.next]
	m.next++
	return r, nil
}
func Evaluate(ctx context.Context, a Attempt, steps []json.RawMessage) (investigation.Result, error) {
	if _, err := Definition(a); err != nil {
		return investigation.Result{}, err
	}
	if a.HypothesisInventory != nil {
		return investigation.RunPinnedProfileInventory(ctx, a.Pin, *a.Profile, *a.HypothesisInventory, &fixtureModel{steps: steps})
	}
	if a.Profile != nil {
		return investigation.RunPinnedProfile(ctx, a.Pin, *a.Profile, &fixtureModel{steps: steps})
	}
	return investigation.RunPinned(ctx, a.Pin, &fixtureModel{steps: steps})
}

// Commit preserves stale/expired output for audit. It does not mutate the case.
func Commit(a Attempt, s newscase.State, steps []json.RawMessage, digest string, result investigation.Result, now time.Time) (Attempt, error) {
	if a.Schema != Schema || a.Status != "pending" || !validTime(now) || now.Before(a.StartedAt) || now.Before(s.LastClock) {
		return a, ErrConflict
	}
	normalized, expected, err := CanonicalSteps(steps)
	if err != nil || expected != digest {
		return a, ErrInvalid
	}
	steps = normalized
	a.Status = "completed"
	if !now.Before(a.Deadline) {
		a.Status = "expired"
	} else if !Inspect(a, s).EvidenceCurrent {
		a.Status = "superseded"
	}
	if !ResultMatchesProfile(a, result) || result.Status != "finalized" || result.Authority != "research_only_unqualified" || result.QualificationCredit || !reflect.DeepEqual(result.Subject, a.Pin.Subject) {
		return a, ErrInvalid
	}
	a.CompletedAt = now.UTC()
	a.StepsSHA256 = digest
	a.Steps = []string{}
	for _, step := range steps {
		a.Steps = append(a.Steps, string(step))
	}
	a.Result = &result
	return a, nil
}

// Definition treats records without a profile as legacy news-review v1 only.
func Definition(a Attempt) (investigation.Contract, error) {
	c := investigation.Definition()
	if a.Profile != nil {
		if a.Profile.Validate() != nil {
			return investigation.Contract{}, ErrInvalid
		}
		c = a.Profile.Contract
	}
	if c.TypeID == investigation.ReuseTypeID {
		if a.HypothesisInventory == nil || a.HypothesisInventory.Validate() != nil || a.HypothesisInventory.ReceiverID != a.Pin.Subject.ReceiverID {
			return investigation.Contract{}, ErrInvalid
		}
	} else if a.HypothesisInventory != nil {
		return investigation.Contract{}, ErrInvalid
	}
	return c, nil
}
func ResultMatchesProfile(a Attempt, r investigation.Result) bool {
	c, err := Definition(a)
	if err != nil {
		return false
	}
	schema := investigation.Schema
	if c.TypeID == investigation.MethodTypeID {
		schema = investigation.MethodSchema
	}
	if c.TypeID == investigation.ReuseTypeID {
		schema = investigation.ReuseSchema
	}
	sha := ""
	if a.Profile != nil {
		sha = a.Profile.SHA256
	}
	inventorySHA, catalogueSHA, receiver := "", "", ""
	if a.HypothesisInventory != nil {
		inventorySHA, catalogueSHA, receiver = a.HypothesisInventory.SHA256, a.HypothesisInventory.CatalogueSHA256, a.HypothesisInventory.ReceiverID
	}
	return r.Schema == schema && r.TypeID == c.TypeID && r.ProfileSHA256 == sha && r.HypothesisInventorySHA256 == inventorySHA && r.HypothesisCatalogueSHA256 == catalogueSHA && r.HypothesisReceiverID == receiver
}
