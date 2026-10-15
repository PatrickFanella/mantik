package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/feedcollector"
)

var ErrCollectorIncomplete = errors.New("collector run is incomplete; inspect actual runtime and retained control requests before recovery")
var ErrCollectorStopped = errors.New("collector run stopped; use retained evidence for explicit recovery")

type CollectorSnapshot struct {
	ReceiverID string              `json:"receiver_id"`
	State      feedcollector.State `json:"state"`
}
type CollectorControl interface {
	Snapshot(context.Context) (CollectorSnapshot, error)
	Command(context.Context, string, feedcollector.Command) (feedcollector.Receipt, error)
}
type CollectorReceiver interface {
	Publish(context.Context, string, feedcollector.Fence, Recording) (feedcollector.EvidenceReceipt, error)
	Snapshot(context.Context, []string) (domain.ResearchSnapshot, error)
	SubmitFenced(context.Context, string, feedcollector.Fence, domain.RoutingInput) (domain.RoutingReceipt, error)
}
type collectorBoundReceiver struct {
	receiver CollectorReceiver
	fence    feedcollector.Fence
}

func (r collectorBoundReceiver) Snapshot(ctx context.Context, ids []string) (domain.ResearchSnapshot, error) {
	return r.receiver.Snapshot(ctx, ids)
}
func (r collectorBoundReceiver) Submit(ctx context.Context, id string, i domain.RoutingInput) (domain.RoutingReceipt, error) {
	return r.receiver.SubmitFenced(ctx, id, r.fence, i)
}

type CollectorSpec struct {
	RegularSession *RegularSessionWindow `json:"regular_session,omitempty"`
	Session        SessionSpec           `json:"session"`
	Binding        Binding               `json:"binding"`
	OwnerID        string                `json:"owner_id"`
}
type collectorIntent struct {
	Schema        string                     `json:"schema"`
	JournalID     string                     `json:"journal_id"`
	Spec          CollectorSpec              `json:"spec"`
	Registration  feedcollector.Registration `json:"registration"`
	At            time.Time                  `json:"at"`
	InitialInputs int                        `json:"initial_inputs"`
}
type collectorPublication struct {
	ReceiverID string              `json:"receiver_id"`
	Fence      feedcollector.Fence `json:"fence"`
	Recording  Recording           `json:"recording"`
}

type collectorStep struct {
	ReceiverID string                `json:"receiver_id"`
	Command    feedcollector.Command `json:"command"`
}
type collectorResult struct {
	CalendarPublished bool          `json:"calendar_published,omitempty"`
	StopCode          string        `json:"stop_code,omitempty"`
	Published         int           `json:"published"`
	Schema            string        `json:"schema"`
	IntentSHA256      string        `json:"intent_sha256"`
	State             string        `json:"state"`
	At                time.Time     `json:"at"`
	StopConfirmed     bool          `json:"stop_confirmed"`
	Steps             int           `json:"steps"`
	Session           SessionStatus `json:"session"`
	Delivery          JournalStatus `json:"delivery"`
}
type CollectorStatus struct {
	Calendar            *CollectionStatus `json:"calendar,omitempty"`
	CalendarPublished   bool              `json:"calendar_published,omitempty"`
	StopCode            string            `json:"stop_code,omitempty"`
	Published           int               `json:"published"`
	PublicationRequired bool              `json:"publication_required"`
	ID                  string            `json:"id"`
	State               string            `json:"state"`
	StopConfirmed       bool              `json:"stop_confirmed"`
	Steps               int               `json:"steps"`
	Session             SessionStatus     `json:"session"`
	Delivery            JournalStatus     `json:"delivery"`
}

func collectorName(id, suffix string) string { return "collector-" + id + "-" + suffix + ".json" }

func (s CollectorSpec) validate() error {
	if s.RegularSession != nil && (s.RegularSession.MaxDurationSeconds < 1 || s.RegularSession.MaxDurationSeconds > 3600 || s.Session.IntervalSeconds > 30) {
		return errors.New("regular-session collector requires a 1–3600 second duration and at most 30 second quote interval")
	}
	if err := s.Session.validate(); err != nil {
		return err
	}
	if err := validateBinding(s.Binding); err != nil {
		return err
	}
	if err := feedcollector.ValidateFence(feedcollector.Fence{RegistrationID: s.Binding.CollectorRegistrationID, OwnerID: s.OwnerID, Generation: 1}); err != nil {
		return err
	}
	if s.Session.Collection.Capability != "latest_quotes" || len(s.Session.Collection.Symbols) != 1 {
		return errors.New("collector requires latest quotes for exactly one registered symbol")
	}
	return nil
}

// Collector reports retained history only. It never infers process liveness or
// resumes an interrupted control operation.
func (j *Journal) Collector(id string) (CollectorStatus, error) {
	out := CollectorStatus{ID: id, State: "incomplete"}
	if !capturePattern.MatchString(id) || len(id) > 32 {
		return out, errors.New("invalid collector identity")
	}
	var intent collectorIntent
	digest, err := j.readCollection(collectorName(id, "intent"), &intent)
	if err != nil {
		return out, err
	}
	if (intent.Schema != "mantik-alpaca-collector-v1" && intent.Schema != "mantik-alpaca-collector-v2" && intent.Schema != "mantik-alpaca-collector-v3") || intent.JournalID != j.ID || intent.Spec.Session.ID != id || intent.Spec.validate() != nil || !validTime(intent.At) || intent.InitialInputs < 0 || intent.InitialInputs > 4096 {
		return out, ErrJournalConflict
	}
	windowed := intent.Schema == "mantik-alpaca-collector-v3"
	if windowed != (intent.Spec.RegularSession != nil) {
		return out, ErrJournalConflict
	}
	out.PublicationRequired = intent.Schema != "mantik-alpaca-collector-v1"
	var done collectorResult
	_, err = j.readCollection(collectorName(id, "complete"), &done)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	required := out.PublicationRequired
	expectedSchema := "mantik-alpaca-collector-result-v1"
	if required {
		expectedSchema = "mantik-alpaca-collector-result-v2"
	}
	if windowed {
		expectedSchema = "mantik-alpaca-collector-result-v3"
	}
	if done.Schema != expectedSchema || (!windowed && (done.CalendarPublished || done.StopCode != "")) || done.Published < 0 || done.Published > intent.Spec.Session.Count || (!required && done.Published != 0) || done.IntentSHA256 != digest || !validTime(done.At) || done.At.Before(intent.At) || done.Steps < 1 || done.Steps > 256 || (done.State != "completed" && done.State != "stopped") {
		return out, ErrJournalConflict
	}
	var last *feedcollector.Receipt
	for n := 1; n <= done.Steps; n++ {
		var step collectorStep
		if _, err := j.readCollection(collectorName(id, fmt.Sprintf("step-%03d-request", n)), &step); err != nil {
			return out, err
		}
		if step.ReceiverID != intent.Spec.Binding.ReceiverID || feedcollector.Validate(step.Command) != nil || step.Command.RegistrationID != intent.Registration.ID || step.Command.OwnerID != intent.Spec.OwnerID {
			return out, ErrJournalConflict
		}
		if step.Command.RequestID != fmt.Sprintf("%s-%s-control-%03d", j.ID, id, n) || (n == 1 && step.Command.Operation != "claim") || (n > 1 && step.Command.Operation != "renew" && step.Command.Operation != "stop") {
			return out, ErrJournalConflict
		}
		var receipt feedcollector.Receipt
		_, err := j.readCollection(collectorName(id, fmt.Sprintf("step-%03d-receipt", n)), &receipt)
		if errors.Is(err, os.ErrNotExist) {
			if done.State == "completed" {
				return out, ErrJournalConflict
			}
			continue
		}
		if err != nil {
			return out, err
		}
		if receipt.RequestID != step.Command.RequestID || receipt.Operation != step.Command.Operation || receipt.Entry.Registration != intent.Registration || receipt.Entry.Lease == nil || receipt.Entry.Lease.OwnerID != intent.Spec.OwnerID {
			return out, ErrJournalConflict
		}
		last = &receipt
	}
	if done.StopConfirmed && (last == nil || last.Operation != "stop" || !last.Entry.Lease.Stopped) {
		return out, ErrJournalConflict
	}
	if required {
		if done.State == "completed" && done.Published != intent.Spec.Session.Count {
			return out, ErrJournalConflict
		}
		records, err := j.PublicationRecordings()
		if err != nil {
			return out, err
		}
		for n := 1; n <= done.Published; n++ {
			var request collectorPublication
			var receipt feedcollector.EvidenceReceipt
			if _, err := j.readCollection(collectorName(id, fmt.Sprintf("publication-%03d-request", n)), &request); err != nil {
				return out, err
			}
			if _, err := j.readCollection(collectorName(id, fmt.Sprintf("publication-%03d-receipt", n)), &receipt); err != nil {
				return out, err
			}
			spec := intent.Spec.Session.collection(n)
			if request.ReceiverID != intent.Spec.Binding.ReceiverID || request.Fence.RegistrationID != intent.Registration.ID || request.Fence.OwnerID != intent.Spec.OwnerID || request.Recording.CaptureID != spec.ID || last == nil || request.Fence.Generation != last.Entry.Lease.Generation || validatePublicationReceipt(request.Recording, request.Fence, receipt) != nil {
				return out, ErrJournalConflict
			}
			found := false
			for _, record := range records {
				if reflect.DeepEqual(record, request.Recording) {
					found = true
				}
			}
			if !found {
				return out, ErrJournalConflict
			}
		}
	}
	calendarInputs := 0
	if windowed {
		calendar, inputs, err := j.inspectWindowCalendar(intent, done, last)
		if err != nil {
			return out, err
		}
		out.Calendar = calendar
		calendarInputs = inputs
	}
	if done.State == "completed" {
		current, err := j.Session(id)
		if err != nil {
			return out, err
		}
		if !done.StopConfirmed || current.State != "completed" || !reflect.DeepEqual(current, done.Session) || done.Delivery.Pending != 0 || done.Delivery.Inputs != intent.InitialInputs+intent.Spec.Session.Count+calendarInputs || done.Delivery.Acknowledged != done.Delivery.Inputs || done.Delivery.Binding == nil || *done.Delivery.Binding != intent.Spec.Binding {
			return out, ErrJournalConflict
		}
		if _, err := j.Status(); err != nil {
			return out, err
		}
	}
	return CollectorStatus{Calendar: out.Calendar, CalendarPublished: done.CalendarPublished, StopCode: done.StopCode, Published: done.Published, PublicationRequired: required, ID: id, State: done.State, StopConfirmed: done.StopConfirmed, Steps: done.Steps, Session: done.Session, Delivery: done.Delivery}, nil
}

func (j *Journal) writeCollectionBinding(b Binding) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return j.writeOnce("binding.json", raw)
}

func (j *Journal) RunCollector(ctx context.Context, c *Client, s CollectorSpec, control CollectorControl, receiver CollectorReceiver) (CollectorStatus, error) {
	return j.runCollector(ctx, c, s, control, receiver, wait)
}
func (j *Journal) runCollector(ctx context.Context, c *Client, s CollectorSpec, control CollectorControl, receiver CollectorReceiver, pause func(context.Context, time.Duration) error) (CollectorStatus, error) {
	return j.runCollectorAt(ctx, c, s, control, receiver, pause, time.Now)
}

func (j *Journal) runCollectorAt(ctx context.Context, c *Client, s CollectorSpec, control CollectorControl, receiver CollectorReceiver, pause func(context.Context, time.Duration) error, now func() time.Time) (CollectorStatus, error) {
	out := CollectorStatus{ID: s.Session.ID, State: "incomplete"}
	if err := s.validate(); err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if c == nil || c.feed != s.Session.Collection.Feed || control == nil || receiver == nil {
		return out, errors.New("collector requires matching client and explicit control/receiver")
	}
	if old, err := j.Collector(s.Session.ID); err == nil {
		var intent collectorIntent
		if _, err := j.readCollection(collectorName(s.Session.ID, "intent"), &intent); err != nil {
			return out, err
		}
		if !reflect.DeepEqual(intent.Spec, s) {
			return out, ErrJournalConflict
		}
		if old.State == "completed" {
			return old, nil
		}
		if old.State == "stopped" {
			return old, ErrCollectorStopped
		}
		return old, ErrCollectorIncomplete
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	startedAt := now().UTC()
	windowExtra := 0
	if s.RegularSession != nil {
		windowExtra = 1
	}
	calendarSpec := windowCalendarSpec(s, startedAt)
	initial, err := j.Status()
	if err != nil {
		return out, err
	}
	if initial.Pending != 0 || initial.Uncertain != 0 || (initial.Binding != nil && *initial.Binding != s.Binding) {
		return out, errors.New("collector requires an empty or fully acknowledged journal with the same fenced binding")
	}
	directory, err := j.root.Open(".")
	if err != nil {
		return out, err
	}
	names, err := directory.Readdirnames(20001)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return out, err
	}
	if closeErr != nil {
		return out, closeErr
	}
	if len(names) > 20000 {
		return out, errors.New("journal artifact count exceeds bound")
	}
	collections, sessions := 0, 0
	specs := []CollectionSpec{}
	for n := 1; n <= s.Session.Count; n++ {
		specs = append(specs, s.Session.collection(n))
	}
	if windowExtra != 0 {
		specs = append(specs, calendarSpec)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "collector-"+s.Session.ID+"-") || strings.HasPrefix(name, "session-"+s.Session.ID+"-") {
			return out, ErrJournalConflict
		}
		for _, spec := range specs {
			if name == captureName(spec.recording(Response{}, j.ID)) || strings.HasPrefix(name, "collection-"+spec.ID+"-") {
				return out, ErrJournalConflict
			}
		}

		if strings.HasPrefix(name, "collection-") && strings.HasSuffix(name, "-intent.json") {
			collections++
		}
		if strings.HasPrefix(name, "session-") && strings.HasSuffix(name, "-intent.json") {
			sessions++
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	readCtx, readCancel := context.WithTimeout(ctx, 5*time.Second)
	snapshot, err := control.Snapshot(readCtx)
	readCancel()
	if err != nil {
		return out, err
	}
	entry, exists := snapshot.State.Entries[s.Binding.CollectorRegistrationID]
	r := entry.Registration
	if snapshot.ReceiverID != s.Binding.ReceiverID || !exists || r.Provider != "alpaca" || r.Feed != s.Session.Collection.Feed || r.Symbol != s.Session.Collection.Symbols[0] || r.EvidenceClass != "prospective_market_unqualified" || r.JournalID != j.ID || r.AccessRef != s.Session.Collection.AccessRef || r.LeaseSeconds != 300 {
		return out, errors.New("collector registration must match receiver, journal, access, symbol, feed and a 300-second lease")
	}
	// Reserve the finite run's worst-case control and admission budgets before
	// claiming. Pacing renews every 30 seconds, separately from read/delivery.
	budget := 2 + 2*windowExtra + 2*s.Session.Count + (s.Session.Count-1)*((s.Session.IntervalSeconds+29)/30)
	if initial.Captures+s.Session.Count+windowExtra > MaxCaptures || collections+s.Session.Count+windowExtra > MaxCollections || sessions >= 128 || len(names)+budget*4+(s.Session.Count+windowExtra)*28+8 > 20000 {
		return out, errors.New("journal capacity cannot cover declared collector run")
	}
	if len(snapshot.State.Commands)+budget > feedcollector.MaxCommands || len(snapshot.State.Admissions)+s.Session.Count+2*windowExtra > feedcollector.MaxAdmissions {
		return out, errors.New("collector registry capacity cannot cover declared run")
	}
	intent := collectorIntent{Schema: "mantik-alpaca-collector-v2", JournalID: j.ID, Spec: s, Registration: r, At: startedAt, InitialInputs: initial.Inputs}
	if windowExtra != 0 {
		intent.Schema = "mantik-alpaca-collector-v3"
	}
	if err := j.writeCollection(collectorName(s.Session.ID, "intent"), intent); err != nil {
		return out, err
	}
	digest, err := j.readCollection(collectorName(s.Session.ID, "intent"), &intent)
	if err != nil {
		return out, err
	}
	// Bind before any remote mutation; current and older clients cannot replace
	// these bytes with a manual-delivery binding.
	if err := j.writeCollectionBinding(s.Binding); err != nil {
		return out, err
	}
	steps := 0
	published := 0
	calendarPublished := false
	if s.RegularSession != nil {
		var windowCancel context.CancelFunc
		ctx, windowCancel = context.WithDeadline(ctx, startedAt.Add(time.Duration(s.RegularSession.MaxDurationSeconds)*time.Second))
		defer windowCancel()
	}
	generation := uint64(0)
	command := func(callCtx context.Context, operation string) (feedcollector.Receipt, error) {
		steps++
		cmd := feedcollector.Command{RequestID: fmt.Sprintf("%s-%s-control-%03d", j.ID, s.Session.ID, steps), Operation: operation, RegistrationID: r.ID, OwnerID: s.OwnerID}
		if operation != "claim" {
			cmd.Generation = generation
		}
		name := collectorName(s.Session.ID, fmt.Sprintf("step-%03d-request", steps))
		if err := j.writeCollection(name, collectorStep{s.Binding.ReceiverID, cmd}); err != nil {
			return feedcollector.Receipt{}, err
		}
		callCtx, callCancel := context.WithTimeout(callCtx, 5*time.Second)
		defer callCancel()
		receipt, err := control.Command(callCtx, s.Binding.ReceiverID, cmd)
		if err != nil {
			return receipt, err
		}
		if receipt.RequestID != cmd.RequestID || receipt.Operation != operation || receipt.Entry.Registration != r || receipt.Entry.Lease == nil || receipt.Entry.Lease.OwnerID != s.OwnerID || receipt.Entry.Lease.Generation == 0 || !validTime(receipt.At) {
			return receipt, ErrJournalConflict
		}
		lease := receipt.Entry.Lease
		if operation != "claim" && lease.Generation != generation {
			return receipt, ErrJournalConflict
		}
		if operation == "stop" {
			if !lease.Stopped {
				return receipt, ErrJournalConflict
			}
		} else if lease.Stopped || !lease.ExpiresAt.Equal(receipt.At.Add(300*time.Second)) {
			return receipt, ErrJournalConflict
		}
		if err := j.writeCollection(collectorName(s.Session.ID, fmt.Sprintf("step-%03d-receipt", steps)), receipt); err != nil {
			return receipt, err
		}
		return receipt, nil
	}
	claim, runErr := command(ctx, "claim")
	if runErr == nil {
		generation = claim.Entry.Lease.Generation
	}
	if runErr == nil {
		renew := func(callCtx context.Context) error { _, err := command(callCtx, "renew"); return err }
		paced := func(callCtx context.Context, delay time.Duration) error {
			for delay > 0 {
				if err := renew(callCtx); err != nil {
					return err
				}
				part := min(delay, 30*time.Second)
				if err := pause(callCtx, part); err != nil {
					return err
				}
				delay -= part
			}
			return nil
		}
		bound := collectorBoundReceiver{receiver: receiver, fence: feedcollector.Fence{RegistrationID: r.ID, OwnerID: s.OwnerID, Generation: generation}}
		var calendar *Recording
		gate := func() error { return checkRegularWindow(s, startedAt, calendar, now()) }
		publish := func(callCtx context.Context, spec CollectionSpec, suffix string) (Recording, error) {
			if err := renew(callCtx); err != nil {
				return Recording{}, err
			}
			deliveryCtx, deliveryCancel := context.WithTimeout(callCtx, 2*time.Minute)
			defer deliveryCancel()
			if _, err := j.Flush(deliveryCtx, s.Binding, bound, 16); err != nil {
				return Recording{}, err
			}
			return j.publishCollectorCapture(deliveryCtx, s, bound, spec, suffix)
		}
		if s.RegularSession != nil {
			if runErr = renew(ctx); runErr == nil {
				readCtx, readCancel := context.WithTimeout(ctx, 2*time.Minute)
				_, runErr = j.Collect(readCtx, c, calendarSpec)
				readCancel()
			}
			if runErr == nil {
				var record Recording
				record, runErr = publish(ctx, calendarSpec, "calendar")
				if runErr == nil {
					calendar = &record
					calendarPublished = true
					runErr = gate()
				}
			}
		}
		if runErr == nil && s.RegularSession != nil {
			coverage, coverageErr := collectorCoverage(s, now(), *calendar)
			if coverageErr != nil {
				runErr = windowStop("calendar_unavailable")
			} else {
				var closeCancel context.CancelFunc
				ctx, closeCancel = context.WithDeadline(ctx, coverage.Session.CloseAt)
				defer closeCancel()
			}
		}
		before := func(callCtx context.Context) error {
			if err := gate(); err != nil {
				return err
			}
			if err := renew(callCtx); err != nil {
				return err
			}
			return gate()
		}
		windowPaced := func(callCtx context.Context, delay time.Duration) error {
			if err := gate(); err != nil {
				return err
			}
			if err := paced(callCtx, delay); err != nil {
				return err
			}
			return gate()
		}
		deliver := func(callCtx context.Context) error {
			record, err := publish(callCtx, s.Session.collection(published+1), fmt.Sprintf("%03d", published+1))
			if err != nil {
				return err
			}
			published++
			if s.RegularSession != nil {
				if err := gate(); err != nil {
					return err
				}
				coverage, err := collectorCoverage(s, now(), *calendar, record)
				if err != nil || !coverage.Quote.Usable || !coverage.Quote.RegularSessionEligible {
					return windowStop("quote_unusable")
				}
			}
			return nil
		}
		if runErr == nil {
			out.Session, runErr = j.runControlledSession(ctx, c, s.Session, windowPaced, before, deliver)
			if runErr != nil && s.RegularSession != nil {
				if gateErr := gate(); gateErr != nil {
					runErr = errors.Join(gateErr, runErr)
				}
			}
		}

	}
	// Acquisition status intentionally retains its own failure code; preserve
	// the parent deadline/cancellation as the orchestration stop reason too.
	if s.RegularSession != nil && ctx.Err() != nil {
		runErr = errors.Join(ctx.Err(), runErr)
	}
	// A cancelled parent cannot prevent bounded cleanup. Unknown claim replies
	// have no trusted generation and are never followed by a guessed stop.
	stopConfirmed := false
	if generation != 0 {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, stopErr := command(cleanup, "stop")
		cleanupCancel()
		stopConfirmed = stopErr == nil
		runErr = errors.Join(runErr, stopErr)
	}
	status, statusErr := j.Status()
	runErr = errors.Join(runErr, statusErr)
	state := "stopped"
	if runErr == nil && stopConfirmed && out.Session.State == "completed" && status.Pending == 0 && published == s.Session.Count {
		state = "completed"
	}
	stopCode := ""
	resultSchema := "mantik-alpaca-collector-result-v2"
	if s.RegularSession != nil {
		resultSchema = "mantik-alpaca-collector-result-v3"
		stopCode = collectorWindowStopCode(runErr, state)
	}
	result := collectorResult{CalendarPublished: calendarPublished, StopCode: stopCode, Schema: resultSchema, IntentSHA256: digest, State: state, At: time.Now().UTC(), StopConfirmed: stopConfirmed, Steps: steps, Session: out.Session, Delivery: status, Published: published}
	if err := j.writeCollection(collectorName(s.Session.ID, "complete"), result); err != nil {
		return out, errors.Join(runErr, err)
	}
	out, err = j.Collector(s.Session.ID)
	if state != "completed" {
		runErr = errors.Join(runErr, ErrCollectorStopped)
	}
	return out, errors.Join(runErr, err)
}
