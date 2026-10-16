package news

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceOnlyArticleDoesNotRequireOrAdmitClaims(t *testing.T) {
	a := FixtureArticle()
	raw, _ := json.Marshal(a)
	got, err := ReadArticle(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatal(got, err)
	}
	if _, err := Read(bytes.NewReader(raw)); err == nil {
		t.Fatal("source-only article admitted as an operator recording")
	}
	old, _ := json.Marshal(Fixture())
	if _, err := ReadArticle(bytes.NewReader(old)); err == nil {
		t.Fatal("recording with supplied claims accepted as source-only article")
	}
	if err := a.ValidateClaims(Fixture().Claims); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"claims":[],`, `"extraction_method":"operator_recorded_v1",`, `"qualification_credit":true,`, `"schema":"duplicate",`, `"Schema":"wrong",`} {
		bad := strings.Replace(string(raw), `{`, `{`+field, 1)
		if _, err := ReadArticle(strings.NewReader(bad)); err == nil {
			t.Fatal("ambiguous or authority field accepted", field)
		}
	}
	for _, bad := range []string{string(raw) + " {}", "null", string([]byte{0xff}), strings.Repeat(" ", MaxRecordingBytes+1)} {
		if _, err := ReadArticle(strings.NewReader(bad)); err == nil {
			t.Fatal("malformed or oversized article accepted")
		}
	}
}

func TestArticleSharesSourceGuardsAndCanonicalTimes(t *testing.T) {
	for name, change := range map[string]func(*Article){
		"schema":      func(a *Article) { a.Schema = RecordingSchema },
		"source":      func(a *Article) { a.SourceID = "" },
		"digest":      func(a *Article) { a.Original += "changed" },
		"causal time": func(a *Article) { a.AvailableAt = a.PublishedAt.Add(-time.Second) },
		"receipt":     func(a *Article) { a.ReceivedAt = a.AvailableAt.Add(-time.Second) },
		"qualified":   func(a *Article) { a.EvidenceClass = "qualified" },
		"credentials": func(a *Article) { a.URL = "https://user:secret@example.invalid/" },
		"revision":    func(a *Article) { a.Kind = "correction"; a.Revision = 2 },
		"body bound":  func(a *Article) { a.Original = strings.Repeat("x", (128<<10)+1); a.OriginalSHA256 = SHA256(a.Original) },
	} {
		t.Run(name, func(t *testing.T) {
			a := FixtureArticle()
			change(&a)
			if a.Validate() == nil {
				t.Fatal("invalid article accepted")
			}
		})
	}
	a := FixtureArticle()
	want, _ := a.Canonical()
	a.PublishedAt = a.PublishedAt.In(time.FixedZone("other", -6*3600))
	a.AvailableAt = a.AvailableAt.In(time.FixedZone("other", -6*3600))
	a.ReceivedAt = a.ReceivedAt.In(time.FixedZone("other", -6*3600))
	got, err := a.Canonical()
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("equivalent times changed article pin", err)
	}
	a.Kind, a.Revision = "correction", 2
	a.Supersedes = &RevisionRef{SourceID: a.SourceID, ArticleID: a.ArticleID, Revision: 1, RecordingSHA256: strings.Repeat("a", 64)}
	got, err = a.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	a.Supersedes.RecordingSHA256 = "changed"
	if got.Supersedes.RecordingSHA256 != strings.Repeat("a", 64) {
		t.Fatal("canonical article aliases predecessor")
	}
}
