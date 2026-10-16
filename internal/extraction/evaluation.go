package extraction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const CorpusSchema = "mantik-recorded-extraction-corpus-v1"
const EvaluationSchema = "mantik-recorded-extraction-evaluation-v1"
const MaxEvaluationBytes = 8 << 20
const MaxEvaluationCases = 32

// Expectations are caller annotations, not verified facts or model provenance.
type EvaluationCase struct {
	ID             string       `json:"id"`
	Article        news.Article `json:"article"`
	EventKey       string       `json:"event_key"`
	ArtifactBase64 string       `json:"artifact_base64"`
	Expected       Response     `json:"expected"`
}

type Corpus struct {
	Schema string           `json:"schema"`
	Cases  []EvaluationCase `json:"cases"`
}

type CaseScore struct {
	ID                  string   `json:"id"`
	Expected            Response `json:"expected"`
	Actual              Result   `json:"actual"`
	Exact               bool     `json:"exact"`
	MatchedExpectedIDs  []string `json:"matched_expected_ids"`
	MissingExpectedIDs  []string `json:"missing_expected_ids"`
	UnexpectedActualIDs []string `json:"unexpected_actual_ids"`
}

type EvaluationCounts struct {
	Cases                 int `json:"cases"`
	ExactCases            int `json:"exact_cases"`
	ExpectedExtracted     int `json:"expected_extracted"`
	ExpectedAbstentions   int `json:"expected_abstentions"`
	UnexpectedAbstentions int `json:"unexpected_abstentions"`
	MissedAbstentions     int `json:"missed_abstentions"`
	MatchedClaims         int `json:"matched_claims"`
	MissingClaims         int `json:"missing_claims"`
	UnexpectedClaims      int `json:"unexpected_claims"`
}

type Evaluation struct {
	Schema              string           `json:"schema"`
	CorpusSHA256        string           `json:"corpus_sha256"`
	ContractSHA256      string           `json:"contract_sha256"`
	Counts              EvaluationCounts `json:"counts"`
	Cases               []CaseScore      `json:"cases"`
	Authority           string           `json:"authority"`
	QualificationCredit bool             `json:"qualification_credit"`
	SHA256              string           `json:"sha256"`
}

type evaluationArtifact []byte

func (a evaluationArtifact) Extract(ctx context.Context, _ Frame) ([]byte, error) {
	return []byte(a), ctx.Err()
}

// claimKey compares every claim field except its local ID. List order inside a
// claim remains significant; claim order across a response does not.
func claimKey(c news.Claim) string {
	c.ID = ""
	return hash(c)
}

// Evaluate replays every case, including malformed/abstaining outputs. It has no
// pass threshold, selected subset, provider access, or publication authority.
func Evaluate(ctx context.Context, input Corpus) (Evaluation, error) {
	invalid := errors.New("evaluation requires a bounded corpus with unique IDs, valid annotations and canonical artifact base64")
	if err := ctx.Err(); err != nil {
		return Evaluation{}, err
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > MaxEvaluationBytes || input.Schema != CorpusSchema || len(input.Cases) == 0 || len(input.Cases) > MaxEvaluationCases {
		return Evaluation{}, invalid
	}
	c := copyJSON(input)
	seen := map[string]bool{}
	gold := make([]Result, len(c.Cases))
	artifacts := make([][]byte, len(c.Cases))
	for i := range c.Cases {
		item := &c.Cases[i]
		if !eventKey.MatchString(item.ID) || seen[item.ID] {
			return Evaluation{}, invalid
		}
		seen[item.ID] = true
		a, e := item.Article.Canonical()
		if e != nil {
			return Evaluation{}, invalid
		}
		item.Article = a
		artifact, e := base64.StdEncoding.DecodeString(item.ArtifactBase64)
		if e != nil || len(artifact) > MaxArtifactBytes || base64.StdEncoding.EncodeToString(artifact) != item.ArtifactBase64 {
			return Evaluation{}, invalid
		}
		artifacts[i] = artifact
		g, e := evaluateExpected(ctx, a, item.EventKey, item.Expected)
		if e != nil {
			return Evaluation{}, e
		}
		gold[i] = g
	}
	r := Evaluation{Schema: EvaluationSchema, CorpusSHA256: hash(c), ContractSHA256: hash(Definition()),
		Cases: []CaseScore{}, Authority: "supplied_annotation_agreement_only"}
	for i, item := range c.Cases {
		actual, e := Run(ctx, item.Article, item.EventKey, evaluationArtifact(artifacts[i]))
		if e != nil {
			return Evaluation{}, e
		}
		s := compareEvaluationCase(item.ID, item.Expected, gold[i], actual)
		addEvaluationCounts(&r.Counts, s)
		r.Cases = append(r.Cases, s)
	}
	r.SHA256 = hash(r)
	return r, nil
}

// evaluateExpected accepts structurally valid annotations, not application
// failures masquerading as expected abstentions. It never attests their truth.
func evaluateExpected(ctx context.Context, a news.Article, key string, annotation Response) (Result, error) {
	raw, err := json.Marshal(annotation)
	if err != nil {
		return Result{}, ErrInvalid
	}
	g, err := Run(ctx, a, key, evaluationArtifact(raw))
	if err != nil {
		return Result{}, err
	}
	if g.Outcome != annotation.Outcome || g.ReasonCode != annotation.ReasonCode || len(g.Claims) != len(annotation.Claims) || annotation.Claims == nil {
		return Result{}, ErrInvalid
	}
	for _, check := range g.Checks {
		if check.Status == "failed" {
			return Result{}, ErrInvalid
		}
	}
	return g, nil
}
func compareEvaluationCase(id string, annotation Response, gold, actual Result) CaseScore {
	s := CaseScore{ID: id, Expected: annotation, Actual: actual,
		MatchedExpectedIDs: []string{}, MissingExpectedIDs: []string{}, UnexpectedActualIDs: []string{}}
	used := make([]bool, len(actual.Claims))
	for _, expected := range gold.Claims {
		match := -1
		for j, candidate := range actual.Claims {
			if !used[j] && claimKey(expected) == claimKey(candidate) {
				match = j
				break
			}
		}
		if match < 0 {
			s.MissingExpectedIDs = append(s.MissingExpectedIDs, expected.ID)
		} else {
			used[match] = true
			s.MatchedExpectedIDs = append(s.MatchedExpectedIDs, expected.ID)
		}
	}
	for j, candidate := range actual.Claims {
		if !used[j] {
			s.UnexpectedActualIDs = append(s.UnexpectedActualIDs, candidate.ID)
		}
	}
	s.Exact = actual.Outcome == annotation.Outcome && actual.ReasonCode == annotation.ReasonCode && len(s.MissingExpectedIDs) == 0 && len(s.UnexpectedActualIDs) == 0
	return s
}
func addEvaluationCounts(counts *EvaluationCounts, s CaseScore) {
	counts.Cases++
	if s.Exact {
		counts.ExactCases++
	}
	if s.Expected.Outcome == "extracted" {
		counts.ExpectedExtracted++
		if s.Actual.Outcome == "abstain" {
			counts.UnexpectedAbstentions++
		}
	} else {
		counts.ExpectedAbstentions++
		if s.Actual.Outcome == "extracted" {
			counts.MissedAbstentions++
		}
	}
	counts.MatchedClaims += len(s.MatchedExpectedIDs)
	counts.MissingClaims += len(s.MissingExpectedIDs)
	counts.UnexpectedClaims += len(s.UnexpectedActualIDs)
}

// FixtureCorpus demonstrates exact agreement and a structurally valid but
// semantically wrong assertion. Neither case measures a real extractor.
func FixtureCorpus() Corpus {
	good := FixtureResponse()
	wrong := copyJSON(good)
	wrong.Claims[0].Assertion = "The source reports that revenue fell."
	b, _ := json.Marshal(good)
	w, _ := json.Marshal(wrong)
	return Corpus{Schema: CorpusSchema, Cases: []EvaluationCase{
		{ID: "invented-exact", Article: news.FixtureArticle(), EventKey: "demo-event", ArtifactBase64: base64.StdEncoding.EncodeToString(b), Expected: good},
		{ID: "invented-wrong-assertion", Article: news.FixtureArticle(), EventKey: "demo-event", ArtifactBase64: base64.StdEncoding.EncodeToString(w), Expected: copyJSON(good)},
	}}
}
