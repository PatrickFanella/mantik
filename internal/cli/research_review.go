package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

type recordedReviewModel struct {
	steps []json.RawMessage
	next  int
}

func (m *recordedReviewModel) Next(ctx context.Context, _ investigation.Frame) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.next >= len(m.steps) {
		return nil, io.EOF
	}
	step := m.steps[m.next]
	m.next++
	return step, nil
}
func runResearchReview(ctx context.Context, args []string, out, errOut io.Writer) (retErr error) {
	fs := flag.NewFlagSet("research-review", flag.ContinueOnError)
	fs.SetOutput(errOut)
	journal := fs.String("journal", "", "absolute private news journal")
	event := fs.String("event", "", "exact event-group ID from news-events")
	asOf := fs.String("as-of", "", "explicit RFC3339 receipt cutoff")
	file := fs.String("model-fixture", "", "private JSON array of recorded model artifacts; no provider calls")
	typeID := fs.String("type", investigation.TypeID, "news-review-v1, news-method-review-v1 or local news-reuse-review-v1")
	catalogueFile := fs.String("hypothesis-catalogue", "", "private complete hypotheses JSON export; required for news-reuse-review-v1")
	export := fs.Bool("export-contract", false, "print the immutable news-review contract")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected research-review positional arguments")
	}
	contract, err := investigation.DefinitionFor(*typeID)
	if err != nil {
		return err
	}
	if *export {
		if *journal != "" || *event != "" || *asOf != "" || *file != "" || *catalogueFile != "" {
			return errors.New("contract export cannot be combined with a review")
		}
		return emit(out, contract)
	}
	if (*typeID == investigation.ReuseTypeID) != (*catalogueFile != "") {
		return errors.New("hypothesis-catalogue is required only for news-reuse-review-v1")
	}
	cutoff, err := time.Parse(time.RFC3339Nano, *asOf)
	if err != nil || *journal == "" || *event == "" || *file == "" {
		return errors.New("research-review requires journal, event, as-of and model-fixture")
	}
	f, err := os.Open(*file)
	if err != nil {
		return errors.New("cannot open model fixture")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (128<<10)+1))
	if err := errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if len(raw) > 128<<10 {
		return errors.New("model fixture exceeds 128 KiB")
	}
	var steps []json.RawMessage
	if err := json.Unmarshal(raw, &steps); err != nil || len(steps) < 1 || len(steps) > 16 {
		return errors.New("model fixture requires 1..16 JSON artifacts")
	}
	j, err := news.OpenExistingJournal(*journal)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, j.Close()) }()
	model := &recordedReviewModel{steps: steps}
	var result investigation.Result
	if *typeID == investigation.ReuseTypeID {
		catalogue, readErr := readReviewCatalogue(*catalogueFile)
		if readErr != nil {
			return readErr
		}
		inventory, pinErr := catalogue.ReviewInventory()
		if pinErr != nil {
			return pinErr
		}
		result, err = investigation.RunWithInventory(ctx, j, *event, cutoff, inventory, model)
	} else {
		result, err = investigation.RunWithType(ctx, j, *event, cutoff, *typeID, model)
	}
	if err != nil {
		return err
	}
	return emit(out, struct {
		Mode   string               `json:"mode"`
		Result investigation.Result `json:"result"`
	}{Mode: "recorded_model_fixture", Result: result})
}

func readReviewCatalogue(path string) (hypothesis.Catalogue, error) {
	var c hypothesis.Catalogue
	f, err := os.Open(path)
	if err != nil {
		return c, errors.New("cannot open hypothesis catalogue")
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
		_ = f.Close()
		return c, errors.New("hypothesis catalogue requires a regular file of at most 4 MiB")
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err = errors.Join(readErr, f.Close()); err != nil {
		return c, err
	}
	if err = workerplan.ReadBoundedShape(raw, &c, 4<<20); err != nil {
		return c, err
	}
	return c, c.Validate()
}
