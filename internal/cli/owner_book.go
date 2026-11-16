package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/account"
	"git.subcult.tv/subculture-collective/mantik/internal/service"
)

var ownerBookID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func runOwnerBook(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("book", flag.ContinueOnError)
	fs.SetOutput(errOut)
	base := fs.String("api-url", defaultURL, "explicit trusted owner service origin; HTTPS or loopback HTTP; no redirects")
	book := fs.String("id", "", "capped paper book ID")
	action := fs.String("action", "show", "show, pause_strategy, resume_strategy, retire_strategy, deposit or owner_clear_halt")
	id := fs.String("action-id", "", "immutable action ID; deposits use deposit-YYYY-MM-DD of the due schedule slot")
	version := fs.Int64("expected-version", -1, "required journal version seen and approved by owner; exact retries retain this value")
	strategy := fs.String("strategy-id", "", "retained strategy version for pause/resume/retire")
	amount := fs.String("amount", "", "deposit only: actual received USD, fixed 500")
	effective := fs.String("effective-at", "", "deposit only: actual cash receipt RFC3339 clock, never an assumed schedule time")
	source := fs.String("source", "", "deposit only: non-secret receipt reference; stored permanently")
	confirm := fs.String("confirm", "", "exact concrete confirmation pin printed by preview; never yes")
	preview := fs.Bool("preview", false, "print exact terms and confirmation pin without sending a mutation")
	format := fs.String("format", "terminal", "terminal or json")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errors.New("invalid book flags; use book --help")
	}
	u, err := url.Parse(*base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) || !ownerBookID.MatchString(*book) || fs.NArg() != 0 || *format != "terminal" && *format != "json" {
		return errors.New("book requires an explicit trusted HTTPS or loopback HTTP origin, book ID and flags only")
	}
	// This credential is provisioned separately from model runtimes. Never accept
	// bearer values in flags, generic body identity, or direct database possession.
	if strings.TrimSpace(os.Getenv("MANTIK_API_TOKEN")) == "" {
		return errors.New("book requires the configured owner's MANTIK_API_TOKEN session; model credentials and direct database access do not authorize owner actions")
	}
	path := "/v1/capped-books/" + *book
	if *action == "show" {
		if *id != "" || *version != -1 || *strategy != "" || *amount != "" || *effective != "" || *source != "" || *confirm != "" || *preview {
			return errors.New("show does not accept action terms")
		}
		var v account.OwnerBookView
		if err := ownerBookRequest(ctx, *base, path+"/owner-view", nil, &v); err != nil {
			return err
		}
		if v.Config.ID != *book {
			return errors.New("owner readback book identity differs")
		}
		return outputOwnerBook(out, v, *format)
	}
	terms := account.OwnerBookAction{ExpectedVersion: *version, Action: *action, StrategyID: *strategy, Amount: *amount, Source: *source}
	if *effective != "" {
		terms.EffectiveAt, err = time.Parse(time.RFC3339Nano, *effective)
		if err != nil {
			return errors.New("invalid actual deposit clock")
		}
		terms.EffectiveAt = terms.EffectiveAt.UTC()
	}
	terms.Confirmation = terms.ConfirmationPin(*book, *id)
	if err := terms.Validate(*book, *id); err != nil {
		return err
	}
	if _, err = fmt.Fprintf(out, "OWNER ACTION book=%q id=%q expected_version=%d\n", *book, *id, *version); err != nil {
		return err
	}
	if err = emit(out, terms); err != nil {
		return err
	}
	if *preview {
		return nil
	}
	if *confirm == "" {
		if _, err = fmt.Fprintln(out, "Type the exact Confirmation pin above, then Enter (EOF cancels):"); err != nil {
			return err
		}
		// ReadString deliberately rejects even a complete pin without a newline at
		// EOF. No default yes, partial input or failed reader authorizes mutation.
		line, e := readOwnerConfirmation(ctx, in)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil || strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r") != terms.Confirmation {
			return errors.New("owner action cancelled; no mutation sent")
		}
	} else if *confirm != terms.Confirmation {
		return errors.New("confirmation pin differs; no mutation sent")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	var receipt service.OwnerBookReceipt
	if err = ownerBookRequest(ctx, *base, path+"/owner-controls/"+*id, terms, &receipt); err != nil {
		return fmt.Errorf("%w; outcome may be unknown: retry the identical action ID, version and terms, then read book", err)
	}
	if receipt.ActionID != *id || receipt.AppliedVersion != *version+1 || receipt.Current.Config.ID != *book || receipt.Current.Version < receipt.AppliedVersion {
		return errors.New("action receipt differs; outcome unknown, inspect book and retry identical terms")
	}
	// GET independently after the acknowledgement. A concurrent re-halt is shown
	// as current state, never hidden behind the successful clear receipt.
	var v account.OwnerBookView
	if err = ownerBookRequest(ctx, *base, path+"/owner-view", nil, &v); err != nil {
		return fmt.Errorf("action recorded at version %d; current readback unavailable: %w", receipt.AppliedVersion, err)
	}
	if v.Config.ID != *book || v.Version < receipt.Current.Version {
		return errors.New("action recorded; current readback regressed or changed book")
	}
	if _, err = fmt.Fprintf(out, "ACTION RECORDED id=%q applied_version=%d; authoritative current readback follows\n", *id, receipt.AppliedVersion); err != nil {
		return err
	}
	return outputOwnerBook(out, v, *format)
}

func readOwnerConfirmation(ctx context.Context, in io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type readResult struct {
		line string
		err  error
	}
	done := make(chan readResult, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(in, 512)).ReadString('\n')
		done <- readResult{line, err}
	}()
	select {
	case result := <-done:
		return result.line, result.err
	case <-ctx.Done():
		// The command is abandoning this confirmation input. Release a blocked
		// terminal or pipe read as well as returning without any owner mutation.
		if closer, ok := in.(io.Closer); ok {
			_ = closer.Close()
		}
		return "", ctx.Err()
	}
}

func ownerBookRequest(ctx context.Context, base, path string, input, output any) error {
	// Existing transport bounds bodies and refuses every redirect. Sanitize
	// remote errors so a misconfigured endpoint cannot echo credentials to logs.
	err := requestBounded(ctx, base, path, input, output, nil, 8<<20, true)
	if err == nil {
		return nil
	}
	var failure *httpFailure
	if errors.As(err, &failure) {
		return fmt.Errorf("owner service refused request (HTTP %d)", failure.Status)
	}
	return errors.New("owner service request or bounded readback failed")
}

func outputOwnerBook(out io.Writer, v account.OwnerBookView, format string) error {
	if format == "json" {
		return emit(out, v)
	}
	// Render through one buffer to propagate output failure consistently. %q
	// escapes retained source/reason strings rather than executing terminal codes.
	var b strings.Builder
	fmt.Fprintf(&b, "CURRENT LOCAL PAPER BOOK %q version=%d as_of=%s\n", v.Config.ID, v.Version, v.AsOf.Format(time.RFC3339Nano))
	fmt.Fprintf(&b, "Cash=%s settled_cash=%s | fixed opening=%s deposits=%s every %d weeks\n", v.Cash, v.SettledCash, v.Config.OpeningCash, v.Config.DepositAmount, v.Config.EveryWeeks)
	if v.EquityUnavailable != "" {
		fmt.Fprintf(&b, "Equity=UNAVAILABLE reason=%q\n", v.EquityUnavailable)
	} else {
		fmt.Fprintf(&b, "Equity=%s (local retained marks; no broker valuation or return claim)\n", v.Equity)
	}
	fmt.Fprintf(&b, "CONTROLS halted=%t paused=%t reason=%q kill=%t reduce_only=%t\n", v.Halted, v.Risk.Paused, v.Risk.PauseReason, v.Risk.Kill, v.Risk.ReduceOnly)
	if v.Halted {
		fmt.Fprintln(&b, "CURRENT REFUSAL risk_hard_halt")
	}
	if v.Risk.Kill {
		fmt.Fprintln(&b, "CURRENT REFUSAL risk_kill_switch")
	}
	if v.Risk.ReduceOnly {
		fmt.Fprintln(&b, "CURRENT REFUSAL risk_reduce_only")
	}
	if v.Risk.Paused {
		fmt.Fprintf(&b, "CURRENT REFUSAL %q\n", v.Risk.PauseReason)
	}
	symbols := []string{}
	for symbol := range v.Positions {
		symbols = append(symbols, symbol)
	}
	slices.Sort(symbols)
	for _, symbol := range symbols {
		fmt.Fprintf(&b, "holding=%q quantity=%q\n", symbol, v.Positions[symbol])
	}
	fmt.Fprintln(&b, "SLEEVES / LIFECYCLE / REFUSALS (latest finalized evidence; historical budget is not current admission)")
	for _, s := range v.Sleeves {
		fmt.Fprintf(&b, "sleeve=%q strategy=%q stage=%q paused=%t permanently_retired=%t\n", s.Sleeve.ID, s.Sleeve.StrategyID, s.Sleeve.Stage, s.Paused, s.Retired)
		fmt.Fprintf(&b, "  gate=%q model_driven=%t\n", s.Sleeve.GateID, s.Sleeve.ModelDriven)
		if s.Paused {
			fmt.Fprintln(&b, "  CURRENT REFUSAL risk_strategy_breaker")
		}
		if s.Retired {
			fmt.Fprintln(&b, "  CURRENT REFUSAL risk_strategy_retired")
		}
		if s.LastAllocation != nil {
			a := s.LastAllocation
			fmt.Fprintf(&b, "  last_budget=%q status=%q reason=%q\n", a.Budget, a.Status, a.Reason)
		} else {
			fmt.Fprintln(&b, "  allocation evidence unavailable")
		}
		if s.LastReview != nil {
			r := s.LastReview
			fmt.Fprintf(&b, "  review=%q at=%s scale=%q reasons=%q\n", r.Action, r.Input.At.Format(time.RFC3339Nano), r.Scale, r.Reasons)
		} else {
			fmt.Fprintln(&b, "  review evidence unavailable")
		}
	}
	fmt.Fprintln(&b, "DEPOSITS (legacy scheduled fixture flows remain distinct from owner-recorded real receipts)")
	for _, d := range v.Deposits {
		source := "legacy scheduled paper flow; no real-cash attestation"
		if d.OwnerAction != nil {
			source = d.OwnerAction.Source
		}
		fmt.Fprintf(&b, "id=%q amount=%q economic_at=%s recorded_at=%s source=%q owner_recorded_real=%t\n", d.ID, d.Amount, d.EffectiveAt.Format(time.RFC3339Nano), d.At.Format(time.RFC3339Nano), source, d.OwnerAction != nil)
	}
	_, err := io.WriteString(out, b.String())
	return err
}
