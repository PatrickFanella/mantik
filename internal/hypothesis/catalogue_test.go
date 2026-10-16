package hypothesis

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

func cataloguePublication(t *testing.T, id string, horizon int64) Publication {
	t.Helper()
	d := Fixture()
	d.HorizonSeconds = horizon
	s, err := Seal(d)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 15, 1, 0, 0, 0, time.UTC)
	return Publication{Schema: PublicationSchema, ID: id, ReceiverID: "receiver-abcdefghijklmnop", ReviewID: "review", ReviewResultID: "review-" + s.DeclarationSHA256, Subject: investigation.Subject{ReceiverID: "receiver-abcdefghijklmnop", AsOf: at}, PublishedAt: at, OriginBindingVerified: true, Sealed: s}
}

func TestCatalogueCanonicalCompleteAndDetached(t *testing.T) {
	a := cataloguePublication(t, "z", 3600)
	b := cataloguePublication(t, "a", 3600)
	b.QuestionReused = true
	b.Sealed.Declaration.Label = "second-source-label"
	b.Sealed, _ = Seal(b.Sealed.Declaration)
	c := cataloguePublication(t, "other", 3601)
	first, err := MakeCatalogue(a.ReceiverID, []Publication{a, c, b})
	if err != nil || first.Validate() != nil || first.QuestionCount != 2 || first.PublicationCount != 3 {
		t.Fatal(first, err)
	}
	reordered, err := MakeCatalogue(a.ReceiverID, []Publication{b, a, c})
	if err != nil || !reflect.DeepEqual(first, reordered) {
		t.Fatal("input order changed catalogue", err)
	}
	for _, q := range first.Questions {
		if q.ID == a.Sealed.ID && (len(q.Publications) != 2 || q.Publications[0].ID != "a" || q.Publications[1].ID != "z") {
			t.Fatal("reuse lineage lost or unordered", q)
		}
	}
	a.Sealed.Declaration.Methods[0].SHA256 = "changed"
	if first.Validate() != nil {
		t.Fatal("caller mutated snapshot")
	}
	empty, err := MakeCatalogue(first.ReceiverID, nil)
	if err != nil || empty.Validate() != nil || empty.Questions == nil || empty.QuestionCount != 0 || empty.PublicationCount != 0 {
		t.Fatal(empty, err)
	}
	all := []Publication{}
	for i := range MaxPublications {
		all = append(all, cataloguePublication(t, fmt.Sprintf("p-%d", i), 3600))
	}
	full, err := MakeCatalogue(first.ReceiverID, all)
	if err != nil || full.Validate() != nil || full.PublicationCount != MaxPublications {
		t.Fatal("full catalogue rejected", err)
	}
	all = append(all, cataloguePublication(t, "overflow", 3600))
	if _, err = MakeCatalogue(first.ReceiverID, all); err == nil {
		t.Fatal("oversized catalogue truncated or accepted")
	}
}

func TestCatalogueRejectsCorruption(t *testing.T) {
	p := cataloguePublication(t, "one", 3600)
	valid, err := MakeCatalogue(p.ReceiverID, []Publication{p})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Catalogue){
		"hash":               func(c *Catalogue) { c.SHA256 = "changed" },
		"count":              func(c *Catalogue) { c.PublicationCount++ },
		"authority":          func(c *Catalogue) { c.QualificationCredit = true },
		"question":           func(c *Catalogue) { c.Questions[0].ID = "changed" },
		"question-authority": func(c *Catalogue) { c.Questions[0].QualificationCredit = true },
		"receiver":           func(c *Catalogue) { c.ReceiverID = "receiver-other_receiver_1" },
		"duplicate":          func(c *Catalogue) { c.Questions[0].Publications = append(c.Questions[0].Publications, p) },
		"sealed":             func(c *Catalogue) { c.Questions[0].Publications[0].Sealed.Declaration.Mechanism.Statement = "changed" },
		"method":             func(c *Catalogue) { c.Questions[0].Publications[0].Sealed.Methods[0].Strategy.Quantity++ },
		"binding":            func(c *Catalogue) { c.Questions[0].Publications[0].OriginBindingVerified = false },
		"clock":              func(c *Catalogue) { c.Questions[0].Publications[0].PublishedAt = p.PublishedAt.Add(-time.Second) },
		"empty":              func(c *Catalogue) { c.Questions[0].Publications = nil },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(valid)
			var c Catalogue
			_ = json.Unmarshal(raw, &c)
			mutate(&c)
			if name != "hash" {
				c.SHA256 = ""
				raw, _ = json.Marshal(c)
				c.SHA256 = news.SHA256(string(raw))
			}
			if c.Validate() == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}
