package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"net/url"
	"regexp"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runNewsService(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var receiver, journal, key, id string
	var version int
	var original bool
	if command == "news-submit" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit pinned research receiver identity")
		fs.StringVar(&journal, "journal", "", "absolute private news journal")
		fs.StringVar(&key, "record", "", "retained record key")
	}
	if command == "news-record" {
		fs.StringVar(&key, "record", "", "server-retained record key")
		fs.BoolVar(&original, "original", false, "explicitly include full retained original article")
	}
	if command == "news-close" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit pinned research receiver identity")
		fs.StringVar(&id, "id", "", "news research case ID")
		fs.IntVar(&version, "version", 0, "current evidence version required to close")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return errors.New("api-url must be an HTTP origin without credentials or a path")
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected news service positional arguments")
	}
	digest := regexp.MustCompile(`^[a-f0-9]{64}$`)
	switch command {
	case "news-cases":
		var result storage.NewsCatalog
		if err := request(ctx, *base, "/v1/news-cases", nil, &result); err != nil {
			return err
		}
		return emit(out, result)
	case "news-record":
		if !digest.MatchString(key) {
			return errors.New("news-record requires a retained record key")
		}
		query := url.Values{}
		if original {
			query.Set("original", "true")
		}
		var result jsonNewsInspection
		if err := requestWithQuery(ctx, *base, "/v1/news-recordings/"+key, nil, &result, query, 4<<20); err != nil {
			return err
		}
		return emit(out, result)
	case "news-submit":
		if !storage.ValidReceiverID(receiver) || !digest.MatchString(key) || journal == "" {
			return errors.New("news-submit requires journal, retained record and explicit receiver-id")
		}
		j, err := news.OpenExistingJournal(journal)
		if err != nil {
			return err
		}
		retained, inspectErr := j.Inspect(key)
		if err := errors.Join(inspectErr, j.Close()); err != nil {
			return err
		}
		var receipt newscase.Receipt
		if err := requestWithQuery(ctx, *base, "/v1/news-recordings", retained.Recording, &receipt, url.Values{"receiver_id": {receiver}}, 16384); err != nil {
			return err
		}
		if receipt.RecordKey != key || receipt.RecordingSHA256 != retained.Result.RecordingSHA256 {
			return errors.New("news admission receipt does not match retained source")
		}
		return emit(out, receipt)
	case "news-close":
		if !storage.ValidReceiverID(receiver) || version < 1 || !regexp.MustCompile(`^news-case-[a-f0-9]{64}$`).MatchString(id) {
			return errors.New("news-close requires receiver-id, case id and current evidence version")
		}
		body := struct {
			ReceiverID string `json:"receiver_id"`
			Version    int    `json:"version"`
		}{receiver, version}
		var result newscase.Case
		if err := request(ctx, *base, "/v1/news-cases/"+id+"/close", body, &result); err != nil {
			return err
		}
		return emit(out, result)
	}
	return errors.New("unsupported news service command")
}

type jsonNewsInspection struct {
	RecordKey string           `json:"record_key"`
	Result    news.Result      `json:"result"`
	Receipt   newscase.Receipt `json:"receipt"`
	Original  string           `json:"original,omitempty"`
}
