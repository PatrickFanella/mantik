// Package assessment compares retained native metrics with declared integer
// conditions. Matching a condition grants no statistical or trading authority.
package assessment

import (
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/trial"
)

const Schema = "mantik-native-trial-assessment-v1"

type Matches struct {
	Prediction bool `json:"prediction"`
	Null       bool `json:"null"`
	Refutation bool `json:"refutation"`
}
type Method struct {
	ID      string              `json:"id"`
	SHA256  string              `json:"sha256"`
	Role    string              `json:"role"`
	Metrics *experiment.Metrics `json:"metrics,omitempty"`
	Matches *Matches            `json:"matches,omitempty"`
}
type Result struct {
	Schema              string               `json:"schema"`
	AttemptID           string               `json:"attempt_id"`
	AttemptSHA256       string               `json:"attempt_sha256"`
	IntentSHA256        string               `json:"intent_sha256"`
	RegistrationSHA256  string               `json:"registration_sha256"`
	PublicationSHA256   string               `json:"publication_sha256"`
	DeclarationSHA256   string               `json:"declaration_sha256"`
	HypothesisID        string               `json:"hypothesis_id"`
	RunSHA256           string               `json:"run_sha256,omitempty"`
	NativeResultSHA256  string               `json:"native_result_sha256,omitempty"`
	Status              string               `json:"status"`
	ReasonCode          string               `json:"reason_code"`
	ErrorCode           string               `json:"error_code,omitempty"`
	Prediction          hypothesis.Condition `json:"prediction"`
	Null                hypothesis.Condition `json:"null"`
	Refutation          hypothesis.Condition `json:"refutation"`
	Methods             []Method             `json:"methods"`
	Authority           string               `json:"authority"`
	QualificationCredit bool                 `json:"qualification_credit"`
	SHA256              string               `json:"sha256"`
}

func matches(c hypothesis.Condition, x int64) bool {
	switch c.Comparison {
	case "lt":
		return x < c.Threshold
	case "le":
		return x <= c.Threshold
	case "eq":
		return x == c.Threshold
	case "ne":
		return x != c.Threshold
	case "ge":
		return x >= c.Threshold
	case "gt":
		return x > c.Threshold
	}
	return false
}
func metric(m experiment.Metrics, name string) int64 {
	switch name {
	case "observations":
		return m.Observations
	case "actions":
		return m.Actions
	case "realized_pnl_cents":
		return m.RealizedPnLCents
	default:
		return m.MarkedPnLCents // validated declaration admits only these four
	}
}

// Build consumes authoritative records read in one storage snapshot. No clock,
// execution, mutation, winner selection or external source is involved.
func Build(e trial.Evidence) (Result, error) {
	a, r, p := e.Attempt, e.Registration, e.Publication
	if a.Validate() != nil || r.Validate() != nil || a.TrialID != r.ID || a.ReceiverID != r.ReceiverID || a.RegistrationSHA256 != r.SHA256 || a.RecipeSHA256 != r.Recipe.SHA256 || a.StartedAt.Before(r.RegisteredAt) {
		return Result{}, trial.ErrInvalid
	}
	// Recheck declaration, publication, native method and horizon bindings. The
	// parent receipt remains a pinned lineage reference, not a new registration.
	bound, err := trial.Register(trial.Command{ReceiverID: r.ReceiverID, RequestID: r.ID, PublicationID: r.PublicationID, Recipe: r.Recipe}, p, nil, r.RegisteredAt)
	copy := r
	copy.ParentID, copy.ParentSHA256, copy.SHA256 = "", "", ""
	copy.SHA256 = trial.Hash(copy)
	if err != nil || !reflect.DeepEqual(bound, copy) {
		return Result{}, trial.ErrInvalid
	}
	d := p.Sealed.Declaration
	out := Result{Schema: Schema, AttemptID: a.ID, AttemptSHA256: a.SHA256, IntentSHA256: a.IntentSHA256, RegistrationSHA256: r.SHA256, PublicationSHA256: r.PublicationSHA256, DeclarationSHA256: r.DeclarationSHA256, HypothesisID: r.HypothesisID, Status: "not_evaluable", ReasonCode: "not_executed", Prediction: d.Prediction, Null: d.Null, Refutation: d.Refutation, Methods: []Method{}, Authority: "synthetic_condition_comparison_only"}
	for _, pin := range r.Recipe.Methods {
		out.Methods = append(out.Methods, Method{ID: pin.ID, SHA256: pin.SHA256, Role: "applicable_method"})
	}
	for _, pin := range r.Recipe.Benchmarks {
		out.Methods = append(out.Methods, Method{ID: pin.ID, SHA256: pin.SHA256, Role: "benchmark"})
	}
	seal := func() (Result, error) { out.SHA256 = trial.Hash(out); return out, nil }
	if e.Run == nil {
		if e.Result != "" {
			return Result{}, trial.ErrInvalid
		}
		if a.Status == "cancelled" {
			out.ReasonCode = "cancelled_before_execution"
		}
		return seal()
	}
	run := *e.Run
	if run.Validate() != nil || a.Status != "pending" || run.AttemptID != a.ID || run.TrialID != r.ID || run.ReceiverID != r.ReceiverID || run.IntentSHA256 != a.IntentSHA256 || run.RegistrationSHA256 != r.SHA256 || run.RecipeSHA256 != r.Recipe.SHA256 || run.StartedAt.Before(a.StartedAt) {
		return Result{}, trial.ErrInvalid
	}
	out.RunSHA256, out.NativeResultSHA256, out.ErrorCode = run.SHA256, run.ResultSHA256, run.ErrorCode
	if run.Status == "running" || run.Status == "incomplete" {
		if e.Result != "" {
			return Result{}, trial.ErrInvalid
		}
		out.ReasonCode = "run_" + run.Status
		return seal()
	}
	// Reapply the native result binding checks to the exact retained bytes.
	initial := run
	initial.Status, initial.ErrorCode, initial.ResultSHA256, initial.SHA256 = "running", "", "", ""
	initial.FinishedAt, initial.DeadlineMet, initial.ResultBytes = time.Time{}, false, 0
	initial.SHA256 = trial.Hash(initial)
	finished, err := trial.FinishRun(initial, r, run.InvocationID, e.Result, run.FinishedAt)
	if err != nil || !reflect.DeepEqual(finished, run) {
		return Result{}, trial.ErrInvalid
	}
	var native experiment.Attempt
	if json.Unmarshal([]byte(e.Result), &native) != nil {
		return Result{}, trial.ErrInvalid
	}
	// Missing metric fields must not silently become zero-valued evidence.
	var presence struct {
		Results []struct {
			Metrics map[string]*int64 `json:"metrics"`
		} `json:"results"`
	}
	if json.Unmarshal([]byte(e.Result), &presence) != nil || len(presence.Results) != len(native.Results) {
		return Result{}, trial.ErrInvalid
	}
	for i, m := range native.Results {
		for _, key := range []string{"observations", "actions", "realized_pnl_cents", "marked_pnl_cents"} {
			if presence.Results[i].Metrics[key] == nil {
				return Result{}, trial.ErrInvalid
			}
		}
		if m.Metrics.Observations < 0 || m.Metrics.Actions < 0 {
			return Result{}, trial.ErrInvalid
		}
		metrics := m.Metrics
		out.Methods[i].Metrics = &metrics
	}
	if run.Status == "failed" {
		out.ReasonCode = "run_failed"
		return seal()
	}
	if !run.DeadlineMet {
		out.ReasonCode = "run_completed_after_deadline"
		return seal()
	}
	out.Status, out.ReasonCode = "compared", "completed_within_deadline"
	for i := range r.Recipe.Methods {
		x := metric(*out.Methods[i].Metrics, d.Prediction.Metric)
		out.Methods[i].Matches = &Matches{matches(d.Prediction, x), matches(d.Null, x), matches(d.Refutation, x)}
	}
	return seal()
}
