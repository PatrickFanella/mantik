package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runForwardDeclaration(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs, db, id := gateFlags("forward-declaration", errOut)
	action := fs.String("action", "show", "declare, show or eligibility (no assessment/promotion)")
	input := fs.String("input", "", "forward declaration JSON for declare")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || *id == "" || *db == "" || os.Getenv(*db) == "" {
		return errors.New("forward-declaration requires id and explicit database-env")
	}
	if *action != "declare" && *action != "show" && *action != "eligibility" {
		return errors.New("unknown forward-declaration action")
	}
	var d forward.Declaration
	if *action == "declare" {
		if e := readGateJSON(*input, &d, 128<<10); e != nil {
			return e
		}
		if d.ID != *id || d.Validate() != nil {
			return forward.ErrInvalid
		}
	}
	s, e := storage.OpenPostgres(ctx, os.Getenv(*db))
	if e != nil {
		return e
	}
	defer s.Close()
	switch *action {
	case "declare":
		r, e := s.DeclareForward(ctx, d, nil)
		if e != nil {
			return e
		}
		return emit(out, r)
	case "eligibility":
		r, e := s.ForwardEligibility(ctx, *id, time.Now().UTC())
		if e != nil {
			return e
		}
		return emit(out, r)
	default:
		r, e := s.ForwardDeclaration(ctx, *id)
		if e != nil {
			return e
		}
		return emit(out, r)
	}
}
