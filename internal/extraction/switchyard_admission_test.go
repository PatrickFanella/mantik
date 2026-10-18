package extraction

import (
	"context"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSwitchyardAdmissionBindsCallerReportedProvenance(t *testing.T) {
	p, f := switchyardAdmissionReceipts(t, 200, localReply("openai/gpt-6.1-sol", string(quoteJSON(quoteFixture())), "stop"))
	at := time.Now().UTC()
	a, err := RetainSwitchyard(context.Background(), "receiver-abcdefghijklmnop", "approved", p, &f, at)
	if err != nil {
		t.Fatal(err)
	}
	c := SwitchyardAdmissionCommand{a.ReceiverID, a.ID, a.SHA256, f.Result.SHA256, SwitchyardApprovalCode}
	wrongProvider := c
	wrongProvider.ApprovalCode = LocalApprovalCode
	if _, err := PrepareSwitchyardAdmission(wrongProvider, a); err == nil {
		t.Fatal("local-model approval code accepted for Switchyard evidence")
	}
	r, err := PrepareSwitchyardAdmission(c, a)
	if err != nil {
		t.Fatal(err)
	}
	s, receipt, err := newscase.Admit(newscase.InitialState(), r, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record := s.Records[receipt.RecordKey]
	approval, err := MakeSwitchyardAdmission(c, a, record)
	if err != nil || approval.Validate(a, record) != nil || approval.ValidateMetadata() != nil || news.OperatorRecording(r) || r.Extraction != nil || r.SwitchyardExtraction.CaptureSHA256 != a.SHA256 || r.SwitchyardExtraction.PreparedSHA256 != p.SHA256 || r.SwitchyardExtraction.FinishedSHA256 != f.SHA256 || r.SwitchyardExtraction.ModelArtifactSHA256 != f.ModelArtifact.SHA256 || r.SwitchyardExtraction.CandidateArtifactSHA256 != f.Anchoring.CandidateArtifactSHA256 {
		t.Fatal("lost provenance", err)
	}
	for _, change := range []func(*SwitchyardAdmissionCommand){func(c *SwitchyardAdmissionCommand) { c.ApprovalCode = "" }, func(c *SwitchyardAdmissionCommand) { c.ApprovalCode = ApprovalCode }, func(c *SwitchyardAdmissionCommand) { c.ReceiverID = "receiver-ponmlkjihgfedcba" }, func(c *SwitchyardAdmissionCommand) { c.CaptureID = "other" }, func(c *SwitchyardAdmissionCommand) { c.CaptureSHA256 = strings.Repeat("f", 64) }, func(c *SwitchyardAdmissionCommand) { c.ResultSHA256 = strings.Repeat("f", 64) }} {
		bad := c
		change(&bad)
		if _, err := PrepareSwitchyardAdmission(bad, a); err == nil {
			t.Fatal("changed approval accepted")
		}
	}
	for _, change := range []func(*newscase.Record){func(r *newscase.Record) { r.Recording.Claims[0].Assertion = "changed" }, func(r *newscase.Record) { r.Recording.SwitchyardExtraction.CaptureID = "other" }, func(r *newscase.Record) { r.Result.SwitchyardExtraction.ModelArtifactSHA256 = strings.Repeat("f", 64) }, func(r *newscase.Record) { r.Receipt.AcceptedAt = at.Add(-time.Second) }, func(r *newscase.Record) { r.Receipt.RecordKey = strings.Repeat("f", 64) }} {
		bad := copyJSON(record)
		change(&bad)
		if approval.Validate(a, bad) == nil {
			t.Fatal("changed evidence accepted")
		}
	}
	for _, change := range []func(*SwitchyardAdmission){func(r *SwitchyardAdmission) { r.ExecutionAttested = true }, func(r *SwitchyardAdmission) { r.QualificationCredit = true }, func(r *SwitchyardAdmission) { r.Authority = "reviewed_recorded_claims_only" }, func(r *SwitchyardAdmission) { r.Provenance.CandidateArtifactSHA256 = strings.Repeat("f", 64) }} {
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
	future = SwitchyardReceipt(sealLocal(LocalReceipt(future)))
	imported, err := RetainSwitchyard(context.Background(), a.ReceiverID, "reported-future", p, &future, at)
	if err != nil {
		t.Fatal(err)
	}
	cc := SwitchyardAdmissionCommand{imported.ReceiverID, imported.ID, imported.SHA256, future.Result.SHA256, SwitchyardApprovalCode}
	rr, err := PrepareSwitchyardAdmission(cc, imported)
	if err != nil {
		t.Fatal(err)
	}
	ss, rc, err := newscase.Admit(newscase.InitialState(), rr, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = MakeSwitchyardAdmission(cc, imported, ss.Records[rc.RecordKey]); err != nil {
		t.Fatal("caller clock used as service execution authority", err)
	}
	r.Claims[0].Evidence[0].Quote = "changed"
	if reflect.DeepEqual(r, record.Recording) {
		t.Fatal("alias retained")
	}
	for _, capture := range []struct {
		p SwitchyardReceipt
		f *SwitchyardReceipt
	}{{p, nil}, func() struct {
		p SwitchyardReceipt
		f *SwitchyardReceipt
	} {
		p, f := switchyardAdmissionReceipts(t, 503, []byte("failure"))
		return struct {
			p SwitchyardReceipt
			f *SwitchyardReceipt
		}{p, &f}
	}()} {
		bad, err := RetainSwitchyard(context.Background(), a.ReceiverID, "ineligible", capture.p, capture.f, at)
		if err != nil {
			t.Fatal(err)
		}
		cmd := SwitchyardAdmissionCommand{bad.ReceiverID, bad.ID, bad.SHA256, strings.Repeat("a", 64), SwitchyardApprovalCode}
		if bad.Finished != nil {
			cmd.ResultSHA256 = bad.Finished.Result.SHA256
		}
		if _, err = PrepareSwitchyardAdmission(cmd, bad); err == nil {
			t.Fatal("unknown/failed capture admitted")
		}
	}
}

func switchyardAdmissionReceipts(t *testing.T, status int, body []byte) (SwitchyardReceipt, SwitchyardReceipt) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status); _, _ = w.Write(body) }))
	defer server.Close()
	var all []SwitchyardReceipt
	_, err := RunSwitchyard(context.Background(), news.FixtureArticle(), "event", SwitchyardConfig{Endpoint: server.URL, Model: SwitchyardModel, MaxTokens: 1024, ReasoningEffort: "low"}, "", func(r SwitchyardReceipt) error { all = append(all, r); return nil })
	if err != nil || len(all) != 2 {
		t.Fatal(err)
	}
	return all[0], all[1]
}
