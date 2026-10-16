package extraction

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLocalAdmissionBindsCallerReportedProvenance(t *testing.T) {
	p, f := inspectionReceipts(t, 200, localReply("ollama/fixture", string(quoteJSON(quoteFixture())), "stop"))
	at := time.Now().UTC()
	a, err := RetainLocal(context.Background(), "receiver-abcdefghijklmnop", "approved", p, &f, at)
	if err != nil {
		t.Fatal(err)
	}
	c := LocalAdmissionCommand{a.ReceiverID, a.ID, a.SHA256, f.Result.SHA256, LocalApprovalCode}
	r, err := PrepareLocalAdmission(c, a)
	if err != nil {
		t.Fatal(err)
	}
	s, receipt, err := newscase.Admit(newscase.InitialState(), r, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record := s.Records[receipt.RecordKey]
	approval, err := MakeLocalAdmission(c, a, record)
	if err != nil || approval.Validate(a, record) != nil || approval.ValidateMetadata() != nil || news.OperatorRecording(r) || r.Extraction != nil || r.LocalExtraction.CaptureSHA256 != a.SHA256 || r.LocalExtraction.PreparedSHA256 != p.SHA256 || r.LocalExtraction.FinishedSHA256 != f.SHA256 || r.LocalExtraction.ModelArtifactSHA256 != f.ModelArtifact.SHA256 || r.LocalExtraction.CandidateArtifactSHA256 != f.Anchoring.CandidateArtifactSHA256 {
		t.Fatal("lost provenance", err)
	}
	for _, change := range []func(*LocalAdmissionCommand){func(c *LocalAdmissionCommand) { c.ApprovalCode = "" }, func(c *LocalAdmissionCommand) { c.ApprovalCode = ApprovalCode }, func(c *LocalAdmissionCommand) { c.ReceiverID = "receiver-ponmlkjihgfedcba" }, func(c *LocalAdmissionCommand) { c.CaptureID = "other" }, func(c *LocalAdmissionCommand) { c.CaptureSHA256 = strings.Repeat("f", 64) }, func(c *LocalAdmissionCommand) { c.ResultSHA256 = strings.Repeat("f", 64) }} {
		bad := c
		change(&bad)
		if _, err := PrepareLocalAdmission(bad, a); err == nil {
			t.Fatal("changed approval accepted")
		}
	}
	for _, change := range []func(*newscase.Record){func(r *newscase.Record) { r.Recording.Claims[0].Assertion = "changed" }, func(r *newscase.Record) { r.Recording.LocalExtraction.CaptureID = "other" }, func(r *newscase.Record) { r.Result.LocalExtraction.ModelArtifactSHA256 = strings.Repeat("f", 64) }, func(r *newscase.Record) { r.Receipt.AcceptedAt = at.Add(-time.Second) }, func(r *newscase.Record) { r.Receipt.RecordKey = strings.Repeat("f", 64) }} {
		bad := copyJSON(record)
		change(&bad)
		if approval.Validate(a, bad) == nil {
			t.Fatal("changed evidence accepted")
		}
	}
	for _, change := range []func(*LocalAdmission){func(r *LocalAdmission) { r.ExecutionAttested = true }, func(r *LocalAdmission) { r.QualificationCredit = true }, func(r *LocalAdmission) { r.Authority = "reviewed_recorded_claims_only" }, func(r *LocalAdmission) { r.Provenance.CandidateArtifactSHA256 = strings.Repeat("f", 64) }} {
		bad := copyJSON(approval)
		change(&bad)
		bad.SHA256 = ""
		bad.SHA256 = hash(bad)
		if bad.Validate(a, record) == nil {
			t.Fatal("resealed forgery accepted")
		}
	}
	// A reported finish clock is not a server-owned execution deadline.
	future := copyJSON(f)
	future.CompletedAt = at.Add(24 * time.Hour)
	future = sealLocal(future)
	imported, err := RetainLocal(context.Background(), a.ReceiverID, "reported-future", p, &future, at)
	if err != nil {
		t.Fatal(err)
	}
	cc := LocalAdmissionCommand{imported.ReceiverID, imported.ID, imported.SHA256, future.Result.SHA256, LocalApprovalCode}
	rr, err := PrepareLocalAdmission(cc, imported)
	if err != nil {
		t.Fatal(err)
	}
	ss, rc, err := newscase.Admit(newscase.InitialState(), rr, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = MakeLocalAdmission(cc, imported, ss.Records[rc.RecordKey]); err != nil {
		t.Fatal("caller clock used as service execution authority", err)
	}
	r.Claims[0].Evidence[0].Quote = "changed"
	if reflect.DeepEqual(r, record.Recording) {
		t.Fatal("alias retained")
	}
	for _, capture := range []struct {
		p LocalReceipt
		f *LocalReceipt
	}{{p, nil}, func() struct {
		p LocalReceipt
		f *LocalReceipt
	} {
		p, f := inspectionReceipts(t, 503, []byte("failure"))
		return struct {
			p LocalReceipt
			f *LocalReceipt
		}{p, &f}
	}()} {
		bad, err := RetainLocal(context.Background(), a.ReceiverID, "ineligible", capture.p, capture.f, at)
		if err != nil {
			t.Fatal(err)
		}
		cmd := LocalAdmissionCommand{bad.ReceiverID, bad.ID, bad.SHA256, strings.Repeat("a", 64), LocalApprovalCode}
		if bad.Finished != nil {
			cmd.ResultSHA256 = bad.Finished.Result.SHA256
		}
		if _, err = PrepareLocalAdmission(cmd, bad); err == nil {
			t.Fatal("unknown/failed capture admitted")
		}
	}
}
