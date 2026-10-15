// Package trial binds pre-execution experiment plans to retained publications.
// Registration does not load datasets, run experiments or grant qualification.
package trial

import (
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experimentplan"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

const Schema = "mantik-hypothesis-trial-registration-v1"
const HistorySchema = "mantik-hypothesis-trial-history-v1"
const MaxRegistrations = 32

var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)

var ErrInvalid = errors.New("invalid hypothesis trial registration")
var ErrConflict = errors.New("trial identity, publication, method, horizon, parent or clock conflict")
var ErrNotFound = errors.New("hypothesis trial or publication not found")
var ErrCapacity = errors.New("hypothesis trial registration capacity reached")

type Command struct {
	ReceiverID    string                `json:"receiver_id"`
	RequestID     string                `json:"request_id"`
	PublicationID string                `json:"publication_id"`
	ParentID      string                `json:"parent_id,omitempty"`
	Recipe        experimentplan.Recipe `json:"recipe"`
}
type Registration struct {
	Schema                      string                `json:"schema"`
	ID                          string                `json:"id"`
	ReceiverID                  string                `json:"receiver_id"`
	HypothesisID                string                `json:"hypothesis_id"`
	PublicationID               string                `json:"publication_id"`
	PublicationSHA256           string                `json:"publication_sha256"`
	DeclarationSHA256           string                `json:"declaration_sha256"`
	ParentID                    string                `json:"parent_id,omitempty"`
	ParentSHA256                string                `json:"parent_sha256,omitempty"`
	RegisteredAt                time.Time             `json:"registered_at"`
	Status                      string                `json:"status"`
	Authority                   string                `json:"authority"`
	QualificationCredit         bool                  `json:"qualification_credit"`
	DatasetAvailabilityVerified bool                  `json:"dataset_availability_verified"`
	Recipe                      experimentplan.Recipe `json:"recipe"`
	SHA256                      string                `json:"sha256"`
}
type History struct {
	Schema              string    `json:"schema"`
	HypothesisID        string    `json:"hypothesis_id"`
	Authority           string    `json:"authority"`
	QualificationCredit bool      `json:"qualification_credit"`
	Registrations       []Summary `json:"registrations"`
}

func (c Command) Validate() error {
	if !review.ID.MatchString(c.RequestID) || !review.ID.MatchString(c.PublicationID) || c.ParentID != "" && (!review.ID.MatchString(c.ParentID) || c.ParentID == c.RequestID) || c.Recipe.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
func Hash(value any) string { raw, _ := json.Marshal(value); return news.SHA256(string(raw)) }
func SameRequest(r Registration, c Command) bool {
	return r.ReceiverID == c.ReceiverID && r.PublicationID == c.PublicationID && r.ParentID == c.ParentID && r.Recipe.SHA256 == c.Recipe.SHA256
}
func (r Registration) Validate() error {
	if !review.ID.MatchString(r.PublicationID) || !digest.MatchString(r.PublicationSHA256) || !digest.MatchString(r.DeclarationSHA256) || r.ParentID == "" && r.ParentSHA256 != "" || r.ParentID != "" && (!review.ID.MatchString(r.ParentID) || r.ParentID == r.ID || !digest.MatchString(r.ParentSHA256)) {
		return ErrInvalid
	}
	if r.Schema != Schema || !review.ID.MatchString(r.ID) || !hypothesis.QuestionID.MatchString(r.HypothesisID) || r.Status != "registered" || r.Authority != "research_trial_declaration_only" || r.QualificationCredit || r.DatasetAvailabilityVerified || !validTime(r.RegisteredAt) || r.Recipe.Validate() != nil {
		return ErrInvalid
	}
	expected := r.SHA256
	r.SHA256 = ""
	if expected != Hash(r) {
		return ErrInvalid
	}
	return nil
}
func validTime(at time.Time) bool {
	return !at.IsZero() && at.UTC().Year() > 0 && at.UTC().Year() <= 9999
}

// Register receives authoritative server records. Historical publications remain
// usable after their decision case closes; no claim of current applicability follows.
func Register(c Command, p hypothesis.Publication, parent *Registration, at time.Time) (Registration, error) {
	if err := c.Validate(); err != nil {
		return Registration{}, err
	}
	if p.ID != c.PublicationID || p.ReceiverID != c.ReceiverID || p.Schema != hypothesis.PublicationSchema || !p.OriginBindingVerified || !validTime(p.PublishedAt) || !validTime(at) || at.Before(p.PublishedAt) {
		return Registration{}, ErrConflict
	}
	sealed, err := hypothesis.Seal(p.Sealed.Declaration)
	if err != nil || !reflect.DeepEqual(sealed, p.Sealed) {
		return Registration{}, ErrConflict
	}
	d := sealed.Declaration
	if c.Recipe.WindowEnd.Sub(c.Recipe.WindowStart) != time.Duration(d.HorizonSeconds)*time.Second {
		return Registration{}, ErrConflict
	}
	// Recipe pins include benchmark/cost assumptions; hypothesis pins bind native
	// configurations. Verify both contracts independently rather than comparing hashes.
	for _, method := range c.Recipe.Methods {
		pin, native, err := hypothesis.PinMethod(method.ID)
		if err != nil || method.Definition.Strategy == nil || !reflect.DeepEqual(*method.Definition.Strategy, native.Strategy) || !reflect.DeepEqual(method.Definition.Entry, native.Entry) {
			return Registration{}, ErrConflict
		}
		found := false
		for _, allowed := range d.Methods {
			if allowed == pin {
				found = true
			}
		}
		if !found {
			return Registration{}, ErrConflict
		}
	}
	parentHash := ""
	if c.ParentID != "" {
		if parent == nil || parent.ID != c.ParentID || parent.ReceiverID != c.ReceiverID || parent.HypothesisID != sealed.ID || parent.Validate() != nil || at.Before(parent.RegisteredAt) {
			return Registration{}, ErrConflict
		}
		parentHash = parent.SHA256
	} else if parent != nil {
		return Registration{}, ErrConflict
	}
	recipe, err := experimentplan.Seal(c.Recipe)
	if err != nil {
		return Registration{}, ErrInvalid
	}
	r := Registration{Schema: Schema, ID: c.RequestID, ReceiverID: c.ReceiverID, HypothesisID: sealed.ID, PublicationID: p.ID, PublicationSHA256: Hash(p), DeclarationSHA256: sealed.DeclarationSHA256, ParentID: c.ParentID, ParentSHA256: parentHash, RegisteredAt: at.UTC(), Status: "registered", Authority: "research_trial_declaration_only", Recipe: recipe}
	r.SHA256 = Hash(r)
	return r, nil
}

// Summary keeps the bounded history useful without repeating full recipes.
type Summary struct {
	ID                string    `json:"id"`
	PublicationID     string    `json:"publication_id"`
	DeclarationSHA256 string    `json:"declaration_sha256"`
	ParentID          string    `json:"parent_id,omitempty"`
	RegisteredAt      time.Time `json:"registered_at"`
	Status            string    `json:"status"`
	RecipeSHA256      string    `json:"recipe_sha256"`
	SHA256            string    `json:"sha256"`
}

func Summarize(r Registration) Summary {
	return Summary{ID: r.ID, PublicationID: r.PublicationID, DeclarationSHA256: r.DeclarationSHA256, ParentID: r.ParentID, RegisteredAt: r.RegisteredAt, Status: r.Status, RecipeSHA256: r.Recipe.SHA256, SHA256: r.SHA256}
}
