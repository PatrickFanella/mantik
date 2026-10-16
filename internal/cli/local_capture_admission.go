package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"reflect"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runLocalCaptureAdmission(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	id := fs.String("id", "", "retained local capture ID")
	var receiver, captureSHA, resultSHA string
	var approved bool
	if command == "news-local-admit" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit research receiver")
		fs.StringVar(&captureSHA, "capture-sha", "", "exact reviewed capture SHA-256")
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
	path := "/v1/news-local-captures/" + *id + "/admission"
	var input any
	c := extraction.LocalAdmissionCommand{ReceiverID: receiver, CaptureID: *id, CaptureSHA256: captureSHA, ResultSHA256: resultSHA, ApprovalCode: extraction.LocalApprovalCode}
	if command == "news-local-admit" {
		if !approved || !storage.ValidReceiverID(receiver) || !extractionAdmissionSHA.MatchString(captureSHA) || !extractionAdmissionSHA.MatchString(resultSHA) {
			return extraction.ErrInvalid
		}
		input = struct {
			ReceiverID    string `json:"receiver_id"`
			CaptureSHA256 string `json:"capture_sha256"`
			ResultSHA256  string `json:"result_sha256"`
			ApprovalCode  string `json:"approval_code"`
		}{receiver, captureSHA, resultSHA, extraction.LocalApprovalCode}
		path = "/v1/news-local-captures/" + *id + "/admit"
	} else if command != "news-local-admission" {
		return extraction.ErrInvalid
	}
	var approval extraction.LocalAdmission
	if err = requestWithQuery(ctx, *base, path, input, &approval, nil, 64<<10); err != nil {
		return err
	}
	if approval.ValidateMetadata() != nil || approval.Command.CaptureID != *id || command == "news-local-admit" && !reflect.DeepEqual(approval.Command, c) {
		return extraction.ErrInvalid
	}
	// Verify the receipt against targeted authoritative reads, not metadata alone.
	var a extraction.LocalCapture
	if err = requestWithQuery(ctx, *base, "/v1/news-local-captures/"+*id, nil, &a, nil, extraction.MaxLocalCaptureBytes); err != nil {
		return err
	}
	var view jsonNewsInspection
	if err = requestWithQuery(ctx, *base, "/v1/news-recordings/"+approval.NewsReceipt.RecordKey, nil, &view, nil, 4<<20); err != nil {
		return err
	}
	r, err := extraction.PrepareLocalAdmission(approval.Command, a)
	if err != nil || view.RecordKey != approval.NewsReceipt.RecordKey || approval.Validate(a, newscase.Record{Recording: r, Result: view.Result, Receipt: view.Receipt}) != nil {
		return extraction.ErrInvalid
	}
	return emit(out, approval)
}
