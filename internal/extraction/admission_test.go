package extraction

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
)

func admissionFixture(t *testing.T) (Attempt, AdmissionCommand, news.Recording, newscase.Record, Admission) {
	t.Helper()
	a, _ := Start("receiver-abcdefghijklmnop", "reviewed", news.FixtureArticle(), "reviewed-event", news.FixtureArticle().ReceivedAt.Add(time.Hour))
	raw, _ := json.Marshal(FixtureResponse())
	result, err := EvaluateRecorded(context.Background(), a, raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err = Commit(a, raw, result, func() time.Time { return a.StartedAt.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}
	c := AdmissionCommand{ReceiverID: a.ReceiverID, AttemptID: a.ID, AttemptSHA256: a.SHA256, ResultSHA256: a.Result.SHA256, ApprovalCode: ApprovalCode}
	r, err := PrepareAdmission(c, a)
	if err != nil {
		t.Fatal(err)
	}
	s, receipt, err := newscase.Admit(newscase.InitialState(), r, a.CompletedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record := s.Records[receipt.RecordKey]
	approval, err := MakeAdmission(c, a, record)
	if err != nil {
		t.Fatal(err)
	}
	return a, c, r, record, approval
}
func TestReviewedAdmissionBindsFullRetainedProvenance(t *testing.T) {
	a, c, r, record, approval := admissionFixture(t)
	if approval.Validate(a, record) != nil || approval.ValidateMetadata() != nil || approval.QualificationCredit || r.ExtractionMethod != news.ReviewedExtractionMethod || r.Extraction.AttemptSHA256 != a.SHA256 || r.Extraction.ArtifactSHA256 != a.Result.Artifact.SHA256 || r.Extraction.ArticleSHA256 != a.ArticleSHA256 || news.OperatorRecording(r) {
		t.Fatal("lost provenance or authority fence")
	}
	for name, change := range map[string]func(*AdmissionCommand){
		"no approval":        func(c *AdmissionCommand) { c.ApprovalCode = "" },
		"changed attempt":    func(c *AdmissionCommand) { c.AttemptSHA256 = strings.Repeat("f", 64) },
		"changed result":     func(c *AdmissionCommand) { c.ResultSHA256 = strings.Repeat("f", 64) },
		"different ID":       func(c *AdmissionCommand) { c.AttemptID = "another" },
		"different receiver": func(c *AdmissionCommand) { c.ReceiverID = "receiver-ponmlkjihgfedcba" },
	} {
		t.Run(name, func(t *testing.T) {
			command := c
			change(&command)
			if _, err := PrepareAdmission(command, a); err == nil {
				t.Fatal("changed approval accepted")
			}
		})
	}
	for _, status := range []string{"expired", "pending"} {
		copy := copyJSON(a)
		copy.Status = status
		if status == "expired" {
			copy.CompletedAt = copy.Deadline
		} else {
			copy.CompletedAt = time.Time{}
			copy.Result = nil
		}
		copy = seal(copy)
		command := c
		command.AttemptSHA256 = copy.SHA256
		if copy.Validate() != nil {
			t.Fatal("bad test fixture")
		}
		if _, err := PrepareAdmission(command, copy); !errors.Is(err, ErrConflict) {
			t.Fatal("ineligible attempt admitted", status, err)
		}
	}
	for name, change := range map[string]func(*newscase.Record){
		"claim edit": func(r *newscase.Record) { r.Recording.Claims[0].Assertion = "edited" },
		"relabel as operator": func(r *newscase.Record) {
			r.Recording.Extraction = nil
			r.Recording.ExtractionMethod = "operator_recorded_v1"
		},
		"source link":    func(r *newscase.Record) { r.Recording.Extraction.ResultSHA256 = strings.Repeat("a", 64) },
		"result link":    func(r *newscase.Record) { r.Result.Extraction.AttemptID = "other" },
		"receipt":        func(r *newscase.Record) { r.Receipt.RecordKey = strings.Repeat("a", 64) },
		"approval clock": func(r *newscase.Record) { r.Receipt.AcceptedAt = a.StartedAt },
	} {
		t.Run(name, func(t *testing.T) {
			copy := copyJSON(record)
			change(&copy)
			if approval.Validate(a, copy) == nil {
				t.Fatal("modified source/result/receipt accepted")
			}
		})
	}
	for _, change := range []func(*Admission){func(r *Admission) { r.QualificationCredit = true }, func(r *Admission) { r.Command.ApprovalCode = "automatic" }, func(r *Admission) { r.ArtifactSHA256 = strings.Repeat("a", 64) }} {
		copy := copyJSON(approval)
		change(&copy)
		copy.SHA256 = ""
		copy.SHA256 = hash(copy)
		if copy.Validate(a, record) == nil {
			t.Fatal("resealed forged approval accepted")
		}
	}
	r.Claims[0].Evidence[0].Quote = "changed"
	if reflect.DeepEqual(r, record.Recording) {
		t.Fatal("conversion retained aliases")
	}
}
