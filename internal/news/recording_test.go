package news

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceBindingsRevisionsAndDistribution(t *testing.T) {
	r := Fixture()
	first, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Claims) != 1 || first.Claims[0].RecordingSHA256 != first.RecordingSHA256 || first.Claims[0].OriginalSHA256 != SHA256(r.Original) || first.Claims[0].Independence != "not_assessed" || !first.Claims[0].AvailableAt.Equal(r.AvailableAt) {
		t.Fatal("source binding or uncertainty lost")
	}
	again, _ := Normalize(r)
	if !reflect.DeepEqual(first, again) {
		t.Fatal("same recording produced different claims")
	}
	// Different distribution is preserved even with identical source text and a
	// supplied shared event key; neither copy establishes independence.
	copy := r
	copy.SourceID = "synthetic-syndication"
	distributed, err := Normalize(copy)
	if err != nil || distributed.Claims[0].Key == first.Claims[0].Key || distributed.Claims[0].EventKey != first.Claims[0].EventKey || distributed.Claims[0].Independence != "not_assessed" {
		t.Fatal("syndication became corroboration or lost its source")
	}
	copy = r
	copy.EvidenceClass = "recorded_market_unqualified"
	recorded, _ := Normalize(copy)
	if recorded.Claims[0].Key == first.Claims[0].Key {
		t.Fatal("synthetic and recorded evidence share a claim identity")
	}
	for _, kind := range []string{"correction", "retraction"} {
		next := Fixture()
		next.Kind = kind
		next.Revision = 2
		next.Supersedes = &RevisionRef{SourceID: r.SourceID, ArticleID: r.ArticleID, Revision: 1, RecordingSHA256: first.RecordingSHA256}
		next.AvailableAt = next.AvailableAt.Add(time.Hour)
		next.ReceivedAt = next.ReceivedAt.Add(time.Hour)
		if kind == "retraction" {
			next.Claims[0].Assessment = "retracted"
		}
		out, err := Normalize(next)
		if err != nil || out.Claims[0].Key == first.Claims[0].Key || out.Supersedes.RecordingSHA256 != first.RecordingSHA256 {
			t.Fatalf("revision: %+v %v", out, err)
		}
	}
	// Normalization cannot expose mutable aliases to caller-owned records.
	r.Claims[0].Evidence[0].Quote = "changed"
	r.Claims[0].Entities[0].Name = "changed"
	if first.Claims[0].Evidence[0].Quote == "changed" || first.Claims[0].Entities[0].Name == "changed" {
		t.Fatal("normalized source bindings alias caller data")
	}
}

func TestNewsRejectsBrokenEvidenceAndAuthority(t *testing.T) {
	changes := map[string]func(*Recording){
		"body digest":           func(r *Recording) { r.Original += " changed" },
		"false quote":           func(r *Recording) { r.Claims[0].Evidence[0].Quote = "different source" },
		"negative span":         func(r *Recording) { r.Claims[0].Evidence[0].Start = -1 },
		"out of range":          func(r *Recording) { r.Claims[0].Evidence[0].End = len(r.Original) + 1 },
		"duplicate spans":       func(r *Recording) { r.Claims[0].Evidence = append(r.Claims[0].Evidence, r.Claims[0].Evidence[0]) },
		"qualified":             func(r *Recording) { r.EvidenceClass = "qualified" },
		"unknown uncertainty":   func(r *Recording) { r.Claims[0].Assessment = "certain" },
		"unsupported extractor": func(r *Recording) { r.ExtractionMethod = "model_says_so" },
		"future publication":    func(r *Recording) { r.PublishedAt = r.ReceivedAt.Add(time.Hour) },
		"unavailable source":    func(r *Recording) { r.AvailableAt = r.ReceivedAt.Add(time.Second) },
		"URL credentials":       func(r *Recording) { r.URL = "https://user:password@example.invalid/article" },
		"duplicate claim":       func(r *Recording) { r.Claims = append(r.Claims, r.Claims[0]) },
		"duplicate entity":      func(r *Recording) { r.Claims[0].Entities = append(r.Claims[0].Entities, r.Claims[0].Entities[0]) },
		"duplicate symbol":      func(r *Recording) { r.Claims[0].Symbols = []string{"DEMO", "DEMO"} },
		"missing entities":      func(r *Recording) { r.Claims[0].Entities = nil },
		"bad initial revision":  func(r *Recording) { r.Revision = 2 },
		"missing predecessor":   func(r *Recording) { r.Kind = "correction"; r.Revision = 2 },
		"undeclared retraction": func(r *Recording) { r.Claims[0].Assessment = "retracted" },
		"oversized body": func(r *Recording) {
			r.Original = strings.Repeat("x", (128<<10)+1)
			r.OriginalSHA256 = SHA256(r.Original)
		},
		"too many claims": func(r *Recording) { r.Claims = make([]Claim, 17) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r := Fixture()
			change(&r)
			if _, err := Normalize(r); err == nil {
				t.Fatal("invalid recording accepted")
			}
		})
	}
}

func TestNewsUTF8BoundariesAndCausalIdentity(t *testing.T) {
	r := Fixture()
	r.Original = "Café issuer reported an estimate."
	r.OriginalSHA256 = SHA256(r.Original)
	r.Claims[0].Evidence = []Span{{Start: 0, End: 5, Quote: "Café"}}
	first, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Claims[0].Evidence[0] = Span{Start: 0, End: 4, Quote: r.Original[:4]}
	if _, err := Normalize(r); err == nil {
		t.Fatal("split UTF-8 excerpt accepted")
	}
	r.Claims[0].Evidence = []Span{{Start: 0, End: 5, Quote: "Café"}}
	zone := time.FixedZone("offset", -6*3600)
	r.PublishedAt = r.PublishedAt.In(zone)
	r.AvailableAt = r.AvailableAt.In(zone)
	r.ReceivedAt = r.ReceivedAt.In(zone)
	again, err := Normalize(r)
	if err != nil || again.RecordingSHA256 != first.RecordingSHA256 {
		t.Fatal("same instants changed canonical record identity")
	}
	r.Claims[0].Assertion = "A changed operator assertion"
	changed, _ := Normalize(r)
	if changed.Claims[0].Key != first.Claims[0].Key || changed.RecordingSHA256 == first.RecordingSHA256 {
		t.Fatal("identity/content distinction was lost")
	}
}

func TestNewsReadRejectsAmbiguousJSON(t *testing.T) {
	r := Fixture()
	raw, _ := json.Marshal(r)
	if _, err := Read(strings.NewReader(string(raw))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		string(raw) + " {}",
		strings.Replace(string(raw), `"source_id":`, `"SOURCE_ID":"different","source_id":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1),
		strings.Replace(string(raw), `"id":"demo-estimate"`, `"id":"duplicate","id":"demo-estimate"`, 1),
		strings.Replace(string(raw), `"schema":`, `"unknown":1,"schema":`, 1),
		strings.Repeat(" ", MaxRecordingBytes+1), strings.Repeat("[", 34) + strings.Repeat("]", 34),
	} {
		if _, err := Read(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous, unknown or oversized JSON accepted")
		}
	}
}
