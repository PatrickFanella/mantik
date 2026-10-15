package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runAlpacaPublish(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("alpaca-publish", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, base, receiver, captureID string
	var revision int
	var fence feedcollector.Fence
	fs.StringVar(&root, "journal", "", "existing private journal")
	fs.StringVar(&base, "api-url", defaultURL, "exact receiver HTTP origin")
	fs.StringVar(&receiver, "receiver-id", "", "expected persistent receiver identity")
	fs.StringVar(&captureID, "capture-id", "", "one retained capture identity (required)")
	fs.IntVar(&revision, "revision", 1, "exact retained revision (1–1000000)")
	fs.StringVar(&fence.RegistrationID, "collector-registration-id", "", "registered collector identity")
	fs.StringVar(&fence.OwnerID, "collector-owner-id", "", "current or original publishing owner")
	fs.Uint64Var(&fence.Generation, "collector-generation", 0, "lease generation")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if root == "" || receiver == "" || captureID == "" || revision < 1 || revision > 1000000 || fs.NArg() != 0 || feedcollector.ValidateFence(fence) != nil {
		return errors.New("publication requires journal, receiver, capture identity and complete collector fence")
	}
	j, err := alpaca.OpenExistingJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = j.Close() }()
	status, err := j.Status()
	if err != nil {
		return err
	}
	binding := alpaca.Binding{APIURL: base, ReceiverID: receiver, CollectorRegistrationID: fence.RegistrationID}
	if status.Binding == nil || *status.Binding != binding {
		return alpaca.ErrTargetChanged
	}
	records, err := j.PublicationRecordings()
	if err != nil {
		return err
	}
	var record *alpaca.Recording
	for _, candidate := range records {
		if candidate.CaptureID == captureID && candidate.Revision == revision {
			if record != nil {
				return errors.New("capture identity has multiple retained revisions; publish an unambiguous capture")
			}
			copy := candidate
			record = &copy
		}
	}
	if record == nil {
		return errors.New("capture identity is not retained")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	remote := remoteResearchReceiver{base: base}
	snapshot, err := remote.Snapshot(ctx, nil)
	if err != nil {
		return err
	}
	if snapshot.ID != receiver {
		return alpaca.ErrTargetChanged
	}
	body := struct {
		ReceiverID string              `json:"receiver_id"`
		Fence      feedcollector.Fence `json:"fence"`
		Recording  alpaca.Recording    `json:"recording"`
	}{receiver, fence, *record}
	var receipt storage.FeedEvidenceReceipt
	if err := request(ctx, base, "/v1/feed-evidence", body, &receipt); err != nil {
		return err
	}
	raw, _ := json.Marshal(record)
	var value any
	_ = json.Unmarshal(raw, &value)
	canonical, _ := json.Marshal(value)
	if receipt.RegistrationID != fence.RegistrationID || receipt.Key != fmt.Sprintf("%s-r%d", record.CaptureID, record.Revision) || receipt.RecordingSHA256 != alpaca.Digest(string(canonical)) || receipt.PublishedAt.IsZero() {
		return errors.New("publication receipt did not match the retained capture")
	}
	return emit(out, receipt)
}

func runFeedCoverage(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("feed-coverage", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var base, id string
	fs.StringVar(&base, "api-url", defaultURL, "receiver service origin")
	fs.StringVar(&id, "collector-registration-id", "", "exact registration to inspect")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || feedcollector.ValidateFence(feedcollector.Fence{RegistrationID: id, OwnerID: "inspection", Generation: 1}) != nil {
		return errors.New("coverage inspection requires a collector registration")
	}
	var report json.RawMessage
	if err := request(ctx, base, "/v1/feed-collectors/"+id+"/coverage", nil, &report); err != nil {
		return err
	}
	return emit(out, report)
}
