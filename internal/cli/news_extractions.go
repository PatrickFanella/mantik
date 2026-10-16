package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"net/url"

	"git.subcult.tv/subculture-collective/mantik/internal/extraction"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runNewsExtractions(ctx context.Context, command string, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "service HTTP origin")
	var id, receiver, articleFile, key, modelFile string
	if command != "news-extractions" {
		fs.StringVar(&id, "id", "", "stable extraction attempt ID")
	}
	if command == "news-extraction-start" || command == "news-extraction-complete" {
		fs.StringVar(&receiver, "receiver-id", "", "explicit pinned research receiver")
	}
	if command == "news-extraction-start" {
		fs.StringVar(&articleFile, "article", "", "source-only article JSON file")
		fs.StringVar(&key, "event-key", "", "operator-selected event key")
	}
	if command == "news-extraction-complete" {
		fs.StringVar(&modelFile, "model-fixture", "", "one raw recorded extractor artifact")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	u, err := url.Parse(*base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || fs.NArg() != 0 {
		return errors.New("extraction command requires an HTTP origin and explicit command arguments")
	}
	if command == "news-extractions" {
		var h extraction.History
		if err = requestWithQuery(ctx, *base, "/v1/news-extractions", nil, &h, nil, 1<<20); err != nil {
			return err
		}
		if err = h.Validate(); err != nil {
			return err
		}
		return emit(out, h)
	}
	if !extraction.ID.MatchString(id) {
		return extraction.ErrInvalid
	}
	var input any
	var article news.Article
	var raw []byte
	path := "/v1/news-extractions/" + id
	switch command {
	case "news-extraction":
	case "news-extraction-start":
		if !storage.ValidReceiverID(receiver) || articleFile == "" || key == "" {
			return extraction.ErrInvalid
		}
		raw, err = readExtractionFile(articleFile, news.MaxRecordingBytes)
		if err != nil {
			return err
		}
		article, err = news.ReadArticle(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		input = struct {
			ReceiverID string       `json:"receiver_id"`
			RequestID  string       `json:"request_id"`
			Article    news.Article `json:"article"`
			EventKey   string       `json:"event_key"`
		}{receiver, id, article, key}
		path = "/v1/news-extractions"
	case "news-extraction-complete":
		if !storage.ValidReceiverID(receiver) || modelFile == "" {
			return extraction.ErrInvalid
		}
		raw, err = readExtractionFile(modelFile, extraction.MaxArtifactBytes)
		if err != nil {
			return err
		}
		input = struct {
			ReceiverID     string `json:"receiver_id"`
			ArtifactBase64 string `json:"artifact_base64"`
		}{receiver, base64.StdEncoding.EncodeToString(raw)}
		path += "/complete"
	default:
		return extraction.ErrInvalid
	}
	var a extraction.Attempt
	if err = requestWithQuery(ctx, *base, path, input, &a, nil, 4<<20); err != nil {
		return err
	}
	if a.Validate() != nil || a.ID != id || receiver != "" && a.ReceiverID != receiver {
		return extraction.ErrInvalid
	}
	if command == "news-extraction-start" && !extraction.SameStart(a, receiver, article, key) {
		return extraction.ErrInvalid
	}
	if command == "news-extraction-complete" && (a.Result == nil || a.Result.Artifact.RawBase64 != base64.StdEncoding.EncodeToString(raw)) {
		return extraction.ErrInvalid
	}
	return emit(out, a)
}
