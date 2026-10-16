package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

type modelFunc func(context.Context, Frame) ([]byte, error)

func (f modelFunc) Extract(ctx context.Context, frame Frame) ([]byte, error) { return f(ctx, frame) }
func encoded(v any) []byte                                                   { raw, _ := json.Marshal(v); return raw }
func recorded(raw []byte) Model {
	return modelFunc(func(context.Context, Frame) ([]byte, error) { return raw, nil })
}
func checkAbstention(t *testing.T, r Result, reason string) {
	t.Helper()
	if r.Outcome != "abstain" || r.ReasonCode != reason || len(r.Claims) != 0 || r.Claims == nil || r.Authority != Authority || r.QualificationCredit || len(r.Checks) != len(Definition().RequiredChecks) {
		t.Fatalf("unsafe or incomplete abstention: %+v", r)
	}
}

func TestExtractedClaimsBindOriginalAndOperatorContext(t *testing.T) {
	a := news.FixtureArticle()
	a.Original = "Café issuer reported an estimate."
	a.OriginalSHA256 = news.SHA256(a.Original)
	a.Kind, a.Revision = "correction", 2
	a.Supersedes = &news.RevisionRef{SourceID: a.SourceID, ArticleID: a.ArticleID, Revision: 1, RecordingSHA256: strings.Repeat("a", 64)}
	p := FixtureResponse()
	p.Claims[0].Evidence = []news.Span{{Start: 0, End: 5, Quote: "Café"}}
	raw := encoded(p)
	r, err := Run(context.Background(), a, "operator-event", modelFunc(func(_ context.Context, frame Frame) ([]byte, error) {
		if frame.Article.Original != a.Original || frame.ArticleSHA256 == "" || frame.Contract.Authority != Authority {
			t.Fatal("extractor did not receive pinned original and contract")
		}
		// Extractor callbacks cannot rewrite source or policy through aliases.
		frame.Article.Supersedes.RecordingSHA256 = strings.Repeat("b", 64)
		frame.Contract.RequiredChecks[0] = "ignore_source"
		return raw, nil
	}))
	if err != nil || r.Outcome != "extracted" || len(r.Claims) != 1 || r.Claims[0].EventKey != "operator-event" || r.Article.Supersedes.RecordingSHA256 != strings.Repeat("a", 64) || r.QualificationCredit || r.Authority != Authority {
		t.Fatal(r, err)
	}
	for _, c := range r.Checks {
		if c.Status != "passed" {
			t.Fatal("missing independent check", c)
		}
	}
	retained, err := base64.StdEncoding.DecodeString(r.Artifact.RawBase64)
	if err != nil || string(retained) != string(raw) || r.Artifact.Bytes != len(raw) || !r.Artifact.CaptureComplete || r.Artifact.SHA256 != news.SHA256(string(raw)) {
		t.Fatal("raw artifact bytes were normalized or lost")
	}
	same, err := Run(context.Background(), a, "operator-event", recorded(raw))
	if err != nil || !reflect.DeepEqual(same, r) {
		t.Fatal("identical recorded input did not reproduce receipt", err)
	}
	a.Supersedes.RecordingSHA256 = "changed"
	p.Claims[0].Evidence[0].Quote = "changed"
	if r.Article.Supersedes.RecordingSHA256 != strings.Repeat("a", 64) || r.Claims[0].Evidence[0].Quote != "Café" {
		t.Fatal("output retained caller aliases")
	}
	sealed := r.SHA256
	r.SHA256 = ""
	if news.SHA256(string(encoded(r))) != sealed {
		t.Fatal("report hash does not bind complete result")
	}
}

func TestExtractorRejectsMalformedArtifactsAndAuthority(t *testing.T) {
	valid := string(encoded(FixtureResponse()))
	bad := []string{
		"", "null", "[]", "{", valid + " {}",
		strings.Replace(valid, `"outcome":`, `"outcome":"duplicate","outcome":`, 1),
		strings.Replace(valid, `"outcome":`, `"Outcome":`, 1),
		strings.Replace(valid, `"claims":`, `"risk_policy":"widen","claims":`, 1),
		strings.Replace(valid, `"claims":`, `"qualification_credit":true,"claims":`, 1),
		strings.Replace(valid, `"claims":`, `"narrative":"trade now","claims":`, 1),
		strings.Replace(valid, `"id":`, `"event_key":"model-event","id":`, 1),
		strings.Replace(valid, `"id":`, `"available_at":"2026-01-01T00:00:00Z","id":`, 1),
		strings.Replace(valid, `"id":`, `"source_id":"other-source","id":`, 1),
		`{"outcome":"extracted","reason_code":"claims_extracted","claims":null}`,
		`{"outcome":"activate","reason_code":"claims_extracted","claims":[]}`,
		`{"outcome":"extracted","reason_code":"unknown","claims":[]}`,
		strings.Replace(valid, `"outcome":"extracted"`, `"outcome":"abstain"`, 1),
		strings.Repeat("[", 34) + strings.Repeat("]", 34),
		string([]byte{0xff, 0xfe}), strings.Repeat("x", MaxArtifactBytes+1),
	}
	for i, raw := range bad {
		r, err := Run(context.Background(), news.FixtureArticle(), "fixed", recorded([]byte(raw)))
		if err != nil {
			t.Fatal(i, err)
		}
		checkAbstention(t, r, "malformed_extractor_artifact")
		if r.Artifact.SHA256 != news.SHA256(raw) || r.Artifact.Bytes != len(raw) {
			t.Fatal("rejected artifact lost identity", i)
		}
		if len(raw) <= MaxArtifactBytes {
			retained, _ := base64.StdEncoding.DecodeString(r.Artifact.RawBase64)
			if !r.Artifact.CaptureComplete || string(retained) != raw {
				t.Fatal("malformed bytes were changed or dropped", i)
			}
		} else if r.Artifact.CaptureComplete || r.Artifact.RawBase64 != "" {
			t.Fatal("oversized response falsely claims full capture")
		}
	}
}

func TestInvalidClaimsCannotProducePartialExtraction(t *testing.T) {
	changes := map[string]func(*Response){
		"no claims":             func(p *Response) { p.Claims = []Claim{} },
		"false quote":           func(p *Response) { p.Claims[0].Evidence[0].Quote = "unsupported excerpt" },
		"out of bounds":         func(p *Response) { p.Claims[0].Evidence[0].End++ },
		"negative offset":       func(p *Response) { p.Claims[0].Evidence[0].Start = -1 },
		"certain":               func(p *Response) { p.Claims[0].Assessment = "certain" },
		"duplicate claim":       func(p *Response) { p.Claims = append(p.Claims, p.Claims[0]) },
		"missing entity":        func(p *Response) { p.Claims[0].Entities = nil },
		"duplicate symbol":      func(p *Response) { p.Claims[0].Symbols = []string{"DEMO", "DEMO"} },
		"undeclared retraction": func(p *Response) { p.Claims[0].Assessment = "retracted" },
		"later invalid claim": func(p *Response) {
			c := p.Claims[0]
			c.ID = "second"
			c.Evidence = []news.Span{{Start: 0, End: 1, Quote: "wrong"}}
			p.Claims = append(p.Claims, c)
		},
		"capacity": func(p *Response) { p.Claims = make([]Claim, 17) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p := FixtureResponse()
			change(&p)
			r, err := Run(context.Background(), news.FixtureArticle(), "fixed", recorded(encoded(p)))
			if err != nil {
				t.Fatal(err)
			}
			checkAbstention(t, r, "invalid_claim_bindings")
		})
	}
	a := news.FixtureArticle()
	a.Original = "Café"
	a.OriginalSHA256 = news.SHA256(a.Original)
	p := FixtureResponse()
	p.Claims[0].Evidence = []news.Span{{Start: 0, End: 4, Quote: "Caf"}}
	r, err := Run(context.Background(), a, "fixed", recorded(encoded(p)))
	if err != nil {
		t.Fatal(err)
	}
	checkAbstention(t, r, "invalid_claim_bindings")
}

func TestAbstentionsErrorsAndCooperativeDeadline(t *testing.T) {
	for _, reason := range []string{"insufficient_source", "no_relevant_claims", "ambiguous_source"} {
		r, err := Run(context.Background(), news.FixtureArticle(), "fixed", recorded(encoded(Response{Outcome: "abstain", ReasonCode: reason, Claims: []Claim{}})))
		if err != nil {
			t.Fatal(err)
		}
		checkAbstention(t, r, reason)
		if r.Checks[3].Status != "not_run" {
			t.Fatal("abstention falsely checked claims")
		}
	}
	for _, tc := range []struct {
		err    error
		reason string
	}{{io.EOF, "extractor_incomplete"}, {errors.New("private provider detail"), "extractor_unavailable"}, {context.DeadlineExceeded, "extractor_timeout"}} {
		r, err := Run(context.Background(), news.FixtureArticle(), "fixed", modelFunc(func(context.Context, Frame) ([]byte, error) { return []byte("partial"), tc.err }))
		if err != nil {
			t.Fatal(err)
		}
		checkAbstention(t, r, tc.reason)
		if strings.Contains(string(encoded(r)), "private provider detail") {
			t.Fatal("provider error leaked")
		}
	}
	r, err := Run(context.Background(), news.FixtureArticle(), "fixed", modelFunc(func(ctx context.Context, _ Frame) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("extractor did not receive the contract deadline")
		}
		transport, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		<-transport.Done()
		return []byte("interrupted"), transport.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	checkAbstention(t, r, "extractor_timeout")
}

func TestInvalidSourceOrContextNeverCallsExtractor(t *testing.T) {
	called := false
	m := modelFunc(func(context.Context, Frame) ([]byte, error) { called = true; return nil, nil })
	a := news.FixtureArticle()
	a.Original += " changed"
	if _, err := Run(context.Background(), a, "fixed", m); err == nil || called {
		t.Fatal("invalid source reached extractor")
	}
	if _, err := Run(context.Background(), news.FixtureArticle(), "", m); err == nil || called {
		t.Fatal("missing operator context reached extractor")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, news.FixtureArticle(), "fixed", m); err == nil || called {
		t.Fatal("cancelled command reached extractor")
	}
}

func TestDistributionLateEvidenceAndRetractionStayAttributed(t *testing.T) {
	a := news.FixtureArticle()
	p := FixtureResponse()
	first, err := Run(context.Background(), a, "shared-operator-event", recorded(encoded(p)))
	if err != nil {
		t.Fatal(err)
	}
	a.SourceID = "syndicated-copy"
	a.AvailableAt = a.AvailableAt.Add(24 * time.Hour)
	a.ReceivedAt = a.AvailableAt.Add(time.Minute)
	copy, err := Run(context.Background(), a, "shared-operator-event", recorded(encoded(p)))
	if err != nil || copy.ArticleSHA256 == first.ArticleSHA256 || copy.Article.SourceID == first.Article.SourceID || !copy.Article.AvailableAt.Equal(a.AvailableAt) || copy.Claims[0].EventKey != first.Claims[0].EventKey || copy.QualificationCredit {
		t.Fatal("distribution erased attribution or timing", err)
	}
	a.Revision, a.Kind = 2, "retraction"
	a.Supersedes = &news.RevisionRef{SourceID: a.SourceID, ArticleID: a.ArticleID, Revision: 1, RecordingSHA256: strings.Repeat("a", 64)}
	p.Claims[0].Assessment = "retracted"
	r, err := Run(context.Background(), a, "shared-operator-event", recorded(encoded(p)))
	if err != nil || r.Outcome != "extracted" || r.Claims[0].Assessment != "retracted" || r.Article.Supersedes == nil {
		t.Fatal(r, err)
	}
	p.Claims[0].Assessment = "reported_unverified"
	r, err = Run(context.Background(), a, "shared-operator-event", recorded(encoded(p)))
	if err != nil {
		t.Fatal(err)
	}
	checkAbstention(t, r, "invalid_claim_bindings")
}
