package cli

import (
	"context"
	"errors"
	"flag"
	"io"

	"git.subcult.tv/subculture-collective/mantik/internal/alpaca"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

type remoteCollectorControl struct{ base string }

func (r remoteCollectorControl) Snapshot(ctx context.Context) (alpaca.CollectorSnapshot, error) {
	var out alpaca.CollectorSnapshot
	err := requestWithQuery(ctx, r.base, "/v1/feed-collectors", nil, &out, nil, 8<<20)
	return out, err
}
func (r remoteCollectorControl) Command(ctx context.Context, receiver string, c feedcollector.Command) (feedcollector.Receipt, error) {
	body := struct {
		ReceiverID string                `json:"receiver_id"`
		Command    feedcollector.Command `json:"command"`
	}{receiver, c}
	var out feedcollector.Receipt
	err := request(ctx, r.base, "/v1/feed-collectors", body, &out)
	return out, err
}
func runAlpacaCollector(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	var root, path, credentials, base, receiver, registration, owner, id string
	var windowSeconds int
	fs.StringVar(&root, "journal", "", "absolute private journal directory")
	if command == "alpaca-collector-status" {
		fs.StringVar(&id, "collector-id", "", "retained finite collector identity")
	} else {
		fs.IntVar(&windowSeconds, "regular-session-seconds", 0, "opt in to calendar-first regular-session quotes for 1–3600 seconds (max quote interval 30s)")
		fs.StringVar(&path, "spec", "", "finite quote session spec JSON")
		fs.StringVar(&credentials, "credentials-file", "", "private mode-0600 provider credentials file")
		fs.StringVar(&base, "api-url", defaultURL, "exact receiver service origin")
		fs.StringVar(&receiver, "receiver-id", "", "expected persistent receiver identity")
		fs.StringVar(&registration, "collector-registration-id", "", "existing immutable registration")
		fs.StringVar(&owner, "collector-owner-id", "", "unique logical owner for this finite run")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if root == "" || fs.NArg() != 0 {
		return errors.New("collector requires an absolute journal and no positional arguments")
	}
	if command == "alpaca-collector-status" {
		j, err := alpaca.OpenExistingJournal(root)
		if err != nil {
			return err
		}
		defer func() { _ = j.Close() }()
		status, err := j.Collector(id)
		if err != nil {
			return err
		}
		return emit(out, status)
	}
	if windowSeconds < 0 || windowSeconds > 3600 {
		return errors.New("regular-session-seconds must be 0 (disabled) or 1–3600")
	}
	if path == "" || credentials == "" || receiver == "" || registration == "" || owner == "" {
		return errors.New("collector requires spec, credentials-file, receiver, registration and owner")
	}
	session, err := readSessionSpec(path)
	if err != nil {
		return err
	}
	key, secret, err := collectionCredentials(credentials)
	if err != nil {
		return err
	}
	client, err := alpaca.NewClient(key, secret, session.Collection.Feed)
	if err != nil {
		return err
	}
	j, err := alpaca.OpenExistingJournal(root)
	if err != nil {
		return err
	}
	defer func() { _ = j.Close() }()
	control := remoteCollectorControl{base: base}
	spec := alpaca.CollectorSpec{Session: session, Binding: alpaca.Binding{APIURL: base, ReceiverID: receiver, CollectorRegistrationID: registration}, OwnerID: owner}
	if windowSeconds != 0 {
		spec.RegularSession = &alpaca.RegularSessionWindow{MaxDurationSeconds: windowSeconds}
	}
	status, runErr := j.RunCollector(ctx, client, spec, control, remoteResearchReceiver{base: base})
	return errors.Join(emit(out, status), runErr)
}
