package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"reflect"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

var extractionAdmissionSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

func runExtractionAdmission(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "retained extraction attempt ID")
	var receiver, attemptSHA, resultSHA string
	var approved bool
	if command == "news-extraction-admit" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit research receiver")
		fs.StringVar(&attemptSHA, "attempt-sha", "", "exact reviewed attempt SHA-256")
		fs.StringVar(&resultSHA, "result-sha", "", "exact reviewed result SHA-256")
		fs.BoolVar(&approved, "approve-candidates", false, "explicitly approve the complete retained claim candidates for research admission")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 || !extraction.ID.MatchString(*id) {
		return extraction.ErrInvalid
	}
	path := "/v1/news-extractions/" + *id + "/admission"
	var input any
	c := extraction.AdmissionCommand{ReceiverID: receiver, AttemptID: *id, AttemptSHA256: attemptSHA, ResultSHA256: resultSHA, ApprovalCode: extraction.ApprovalCode}
	if command == "news-extraction-admit" {
		if !approved || !storage.ValidReceiverID(receiver) || !extractionAdmissionSHA.MatchString(attemptSHA) || !extractionAdmissionSHA.MatchString(resultSHA) {
			return extraction.ErrInvalid
		}
		input = struct {
			ReceiverID    string `json:"receiver_id"`
			AttemptSHA256 string `json:"attempt_sha256"`
			ResultSHA256  string `json:"result_sha256"`
			ApprovalCode  string `json:"approval_code"`
		}{receiver, attemptSHA, resultSHA, extraction.ApprovalCode}
		path = "/v1/news-extractions/" + *id + "/admit"
	} else if command != "news-extraction-admission" {
		return extraction.ErrInvalid
	}
	var approval extraction.Admission
	if err = requestWithQuery(ctx, *base, path, input, &approval, nil, 64<<10); err != nil {
		return err
	}
	if approval.ValidateMetadata() != nil || approval.Command.AttemptID != *id || command == "news-extraction-admit" && !reflect.DeepEqual(approval.Command, c) {
		return extraction.ErrInvalid
	}
	// Verify the receipt against targeted authoritative reads, not metadata alone.
	var a extraction.Attempt
	if err = requestWithQuery(ctx, *base, "/v1/news-extractions/"+*id, nil, &a, nil, 4<<20); err != nil {
		return err
	}
	var view jsonNewsInspection
	if err = requestWithQuery(ctx, *base, "/v1/news-recordings/"+approval.NewsReceipt.RecordKey, nil, &view, nil, 4<<20); err != nil {
		return err
	}
	r, err := extraction.PrepareAdmission(approval.Command, a)
	if err != nil || view.RecordKey != approval.NewsReceipt.RecordKey || approval.Validate(a, newscase.Record{Recording: r, Result: view.Result, Receipt: view.Receipt}) != nil {
		return extraction.ErrInvalid
	}
	return emit(out, approval)
}
