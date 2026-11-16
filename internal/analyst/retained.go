package analyst

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/allocation"
	"git.subcult.tv/subculture-collective/mantik/internal/model"
	"git.subcult.tv/subculture-collective/mantik/internal/research"
	compiled "git.subcult.tv/subculture-collective/mantik/internal/strategy/analyst"
	"git.subcult.tv/subculture-collective/mantik/internal/strategytrial"
)

// EvidenceReader exposes retained events without model calls or journal writes.
type EvidenceReader interface {
	Events(context.Context, string) ([]research.Event, error)
}

// Retained reads a completed, immutable runtime result without another model
// call. Interrupted sessions produce no target. The source request remains the
// only source of bars, identity and provenance for the execution bridge.
func Retained(ctx context.Context, journal EvidenceReader, id string) (Request, Result, error) {
	bad := errors.New("retained analyst evidence is unavailable or inconsistent")
	if journal == nil || !strategytrial.ValidID(id) || len(id) > 64 {
		return Request{}, Result{}, bad
	}
	events, err := journal.Events(ctx, "analyst-"+id)
	if err != nil {
		return Request{}, Result{}, err
	}
	if len(events) == 0 || events[0].Kind != "start" {
		return Request{}, Result{}, bad
	}
	var request Request
	if json.Unmarshal(events[0].Body, &request) != nil || request.ID != id || request.Validate(request.AsOf) != nil {
		return Request{}, Result{}, bad
	}
	spec, err := request.Config.Spec()
	if err != nil {
		return Request{}, Result{}, err
	}
	sid, _ := spec.ID()
	last := events[len(events)-1]
	if last.At.IsZero() || last.At.Before(events[0].At) {
		return Request{}, Result{}, bad
	}
	calls := 0
	knownAt := request.AsOf.Truncate(time.Microsecond)
	for _, event := range events {
		if event.At.IsZero() || event.At.Before(knownAt) {
			return Request{}, Result{}, bad
		}
		knownAt = event.At
		if event.Kind == "call" {
			calls++
		}
	}
	if calls > len(roles) {
		return Request{}, Result{}, bad
	}
	result := Result{ID: id, StrategyID: sid, RequestSHA256: hash(marshal(request)), EvidenceClass: request.Data.EvidenceClass, SourceSHA256: request.Data.SourceSHA256, Calls: calls, Status: "no_target", Reason: "interrupted_no_relaunch", Target: allocation.TargetDecision{HoldExposure: true}}
	if last.Kind == "done" {
		// Decode into zero values: omitted false fields must not inherit the
		// default exposure hold used only for interrupted sessions.
		var saved Result
		if err := json.Unmarshal(last.Body, &saved); err != nil {
			return Request{}, Result{}, bad
		}
		result = saved
		if result.ID != id || result.StrategyID != sid || result.RequestSHA256 != hash(marshal(request)) || result.EvidenceClass != request.Data.EvidenceClass || result.SourceSHA256 != request.Data.SourceSHA256 || result.QualificationCredit || result.Calls != calls {
			return Request{}, Result{}, bad
		}
		if result.Status != "target" && result.Status != "no_target" {
			return Request{}, Result{}, bad
		}
	}
	receipt := result.Receipt
	if receipt.Completed {
		if receipt.StrategyID != sid || !receipt.InputAt.Equal(request.AsOf) || receipt.At.Before(request.AsOf) || receipt.At.After(last.At.Add(time.Microsecond-time.Nanosecond)) || receipt.EvidenceClass != result.EvidenceClass || receipt.SourceSHA256 != result.SourceSHA256 {
			return Request{}, Result{}, bad
		}
		v, err := view(ctx, request)
		if err != nil {
			return Request{}, Result{}, err
		}
		_, viewHash := compiled.Input(v)
		if receipt.ViewSHA256 != viewHash {
			return Request{}, Result{}, bad
		}
		if result.Reason != "completed" || calls != len(roles) || len(events) != 2+3*len(roles) || last.Kind != "done" {
			return Request{}, Result{}, bad
		}
		input, _ := compiled.Input(v)
		selections := []compiled.Selection{}
		known := request.AsOf
		for i, role := range roles {
			call, reply, selected := events[1+3*i], events[2+3*i], events[3+3*i]
			prior := selections
			if i < 2 {
				prior = nil
			}
			promptHash := hash([]byte(workflowPrompt(request, role, input, prior)))
			var intent struct {
				Step         int    `json:"step"`
				Role         string `json:"role"`
				PromptSHA256 string `json:"prompt_sha256"`
			}
			var response struct {
				Step     int
				Record   model.Record
				Response string
				Failed   bool
			}
			var selection compiled.Selection
			if call.Kind != "call" || reply.Kind != "reply" || selected.Kind != "result" || json.Unmarshal(call.Body, &intent) != nil || intent.Step != i || intent.Role != role || intent.PromptSHA256 != promptHash || json.Unmarshal(reply.Body, &response) != nil || response.Step != i || response.Failed || json.Unmarshal(selected.Body, &selection) != nil {
				return Request{}, Result{}, bad
			}
			record := response.Record
			parsed, err := compiled.Parse(response.Response, request.Config.Assets)
			// Record.Response is omitted by the subscription transport's JSON.
			// The separately retained exact response must still match its hash.
			if err != nil || parsed != selection || record.Status != "completed" || record.Provider != model.Provider(request.Config.Provider) || record.Model != request.Config.Model || record.PromptSHA256 != promptHash || record.ResponseSHA256 != hash([]byte(response.Response)) || record.StartedAt.Before(request.AsOf) || record.FinishedAt.Before(record.StartedAt) || record.FinishedAt.After(reply.At.Add(time.Microsecond-time.Nanosecond)) {
				return Request{}, Result{}, bad
			}
			for _, event := range []research.Event{call, reply, selected} {
				if event.At.Add(time.Microsecond - time.Nanosecond).Before(known) {
					return Request{}, Result{}, bad
				}
				if event.At.After(known) {
					known = event.At
				}
			}
			if record.FinishedAt.After(known) {
				known = record.FinishedAt
			}
			selections = append(selections, selection)
		}
		if receipt.At.Before(known) || receipt.Selection != selections[len(selections)-1] {
			return Request{}, Result{}, fmt.Errorf("%w: completion precedes evidence or selection differs", bad)
		}
		if _, err := compiled.New(request.Config, receipt); err != nil {
			return Request{}, Result{}, err
		}
	} else {
		if result.Status != "no_target" || !reflect.DeepEqual(receipt, compiled.Receipt{}) {
			return Request{}, Result{}, bad
		}
		receipt = compiled.Receipt{At: last.At, InputAt: request.AsOf, StrategyID: sid, EvidenceClass: result.EvidenceClass, SourceSHA256: result.SourceSHA256}
	}
	expected := allocation.TargetDecision{HoldExposure: true}
	if receipt.Completed && receipt.Selection.Action != "hold" {
		expected = allocation.TargetDecision{}
		if receipt.Selection.Action == "long" {
			expected.Targets = []allocation.WeightTarget{{Asset: receipt.Selection.Asset, Weight: "1"}}
		}
		if result.Status != "target" {
			return Request{}, Result{}, bad
		}
	} else if result.Status != "no_target" {
		return Request{}, Result{}, bad
	}
	if string(marshal(result.Target)) != string(marshal(expected)) {
		return Request{}, Result{}, bad
	}
	receipt.CycleID, receipt.RequestSHA256, receipt.ResultSHA256 = id, result.RequestSHA256, hash(marshal(result))
	receipt.ForwardDeclarationID = request.ForwardDeclarationID
	result.Receipt = receipt
	return request, result, nil
}
