package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"

	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func runForwardAssessment(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs, db, id := gateFlags("forward-assessment", errOut)
	action := fs.String("action", "show", "declare, show or assess")
	input := fs.String("input", "", "pre-window assessment plan JSON")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if fs.NArg() != 0 || *id == "" || *db == "" || os.Getenv(*db) == "" {
		return errors.New("forward-assessment requires id and explicit database-env")
	}
	if *action != "declare" && *action != "show" && *action != "assess" {
		return errors.New("unknown forward-assessment action")
	}
	if *action != "declare" && *input != "" {
		return errors.New("only declare accepts input; assessment derives retained evidence")
	}
	var plan forward.AssessmentPlan
	if *action == "declare" {
		if e := readGateJSON(*input, &plan, 128<<10); e != nil {
			return e
		}
		if plan.ID != *id {
			return forward.ErrInvalid
		}
	}
	s, e := storage.OpenPostgresWithQualificationEvaluator(ctx, os.Getenv(*db), service.EvaluateQualificationCollection)
	if e != nil {
		return e
	}
	defer s.Close()
	switch *action {
	case "declare":
		v, e := s.DeclareForwardAssessment(ctx, plan, nil)
		if e != nil {
			return e
		}
		return emit(out, v)
	case "assess":
		v, e := s.AssessForward(ctx, *id, nil)
		if e != nil {
			return e
		}
		return emit(out, v)
	default:
		v, e := s.ForwardAssessmentPlan(ctx, *id)
		if e != nil {
			return e
		}
		return emit(out, v)
	}
}
