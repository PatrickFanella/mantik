package hypothesis

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
	"git.subcult.tv/subculture-collective/mantik/internal/review"
)

const CatalogueSchema = "mantik-hypothesis-catalogue-v1"

var receiverID = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var reviewResultID = regexp.MustCompile(`^review-[a-f0-9]{64}$`)

// Catalogue is a complete bounded snapshot of published declarations. Its hash
// identifies these bytes, not the truth or current applicability of any claim.
type Catalogue struct {
	Schema              string     `json:"schema"`
	ReceiverID          string     `json:"receiver_id"`
	Questions           []Question `json:"questions"`
	QuestionCount       int        `json:"question_count"`
	PublicationCount    int        `json:"publication_count"`
	Authority           string     `json:"authority"`
	QualificationCredit bool       `json:"qualification_credit"`
	SHA256              string     `json:"sha256"`
}

func MakeCatalogue(receiver string, publications []Publication) (Catalogue, error) {
	if !receiverID.MatchString(receiver) || len(publications) > MaxPublications {
		return Catalogue{}, ErrInvalid
	}
	c := Catalogue{Schema: CatalogueSchema, ReceiverID: receiver, Questions: []Question{}, Authority: "retained_research_declarations_only"}
	groups := map[string][]Publication{}
	seen := map[string]bool{}
	for _, p := range publications {
		sealed, err := Seal(p.Sealed.Declaration)
		if err != nil || !reflect.DeepEqual(sealed, p.Sealed) || p.Schema != PublicationSchema || !review.ID.MatchString(p.ID) || seen[p.ID] || p.ReceiverID != receiver || p.Subject.ReceiverID != receiver || !review.ID.MatchString(p.ReviewID) || !reviewResultID.MatchString(p.ReviewResultID) || !p.OriginBindingVerified || p.PublishedAt.IsZero() || p.PublishedAt.Year() < 1 || p.PublishedAt.Year() > 9999 || p.Subject.AsOf.IsZero() || p.PublishedAt.Before(p.Subject.AsOf) {
			return Catalogue{}, ErrInvalid
		}
		seen[p.ID] = true
		groups[p.Sealed.ID] = append(groups[p.Sealed.ID], p)
	}
	for id, group := range groups {
		slices.SortFunc(group, func(a, b Publication) int {
			if cmp := a.PublishedAt.Compare(b.PublishedAt); cmp != 0 {
				return cmp
			}
			return strings.Compare(a.ID, b.ID)
		})
		c.Questions = append(c.Questions, Question{Schema: QuestionSchema, ID: id, Authority: "research_declaration_only", Publications: group})
	}
	slices.SortFunc(c.Questions, func(a, b Question) int { return strings.Compare(a.ID, b.ID) })
	c.QuestionCount, c.PublicationCount = len(c.Questions), len(publications)
	raw, err := json.Marshal(c)
	if err != nil {
		return Catalogue{}, ErrInvalid
	}
	c.SHA256 = news.SHA256(string(raw))
	// The builder also detaches nested declaration and method slices.
	raw, _ = json.Marshal(c)
	var detached Catalogue
	if json.Unmarshal(raw, &detached) != nil {
		return Catalogue{}, ErrInvalid
	}
	return detached, nil
}

func (c Catalogue) Validate() error {
	if c.Questions == nil || len(c.Questions) > MaxPublications {
		return ErrInvalid
	}
	all := []Publication{}
	for _, q := range c.Questions {
		if len(q.Publications) == 0 || len(q.Publications) > MaxPublications-len(all) {
			return ErrInvalid
		}
		all = append(all, q.Publications...)
	}
	expected, err := MakeCatalogue(c.ReceiverID, all)
	if err != nil || !reflect.DeepEqual(c, expected) {
		return ErrInvalid
	}
	return nil
}

// ReviewInventory validates the complete catalogue before deriving targeted
// declaration-tool inputs. Repeated identical publications share one tool ref;
// their full attribution remains in the catalogue identified by its hash.
func (c Catalogue) ReviewInventory() (question.Inventory, error) {
	if err := c.Validate(); err != nil {
		return question.Inventory{}, err
	}
	declarations := []Result{}
	for _, q := range c.Questions {
		for _, p := range q.Publications {
			declarations = append(declarations, p.Sealed)
		}
	}
	return question.MakeInventory(c.ReceiverID, c.SHA256, declarations)
}
