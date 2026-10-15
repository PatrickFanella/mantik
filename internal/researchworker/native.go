// Package researchworker adapts the native synthetic engine to workerplan.
// It does not launch processes, attest isolation, persist jobs or call a service.
package researchworker

import (
	"context"
	"encoding/json"
	"io"
	"runtime"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/experiment"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func Declare(id, image string, recipe experiment.Recipe, raw []byte) (workerplan.Job, error) {
	var dataset replay.Dataset
	if workerplan.ReadDatasetShape(raw, &dataset) != nil {
		return workerplan.Job{}, workerplan.ErrInvalid
	}
	canonical, err := json.Marshal(dataset)
	if err != nil {
		return workerplan.Job{}, err
	}
	return workerplan.Seal(workerplan.Job{Schema: workerplan.JobSchema, ID: id, Tool: workerplan.Tool, Runtime: workerplan.Runtime, ImageSHA256: image, Recipe: recipe, Input: workerplan.Input{Reference: "dataset.json", MediaType: "application/json", Bytes: len(raw), SHA256: workerplan.Digest(raw), CanonicalSHA256: workerplan.Digest(canonical)}, Resources: workerplan.Profile(), OutputReference: "native-attempt.json", OutputMediaType: "application/json", Authority: "research_worker_declaration_only"})
}
func Execute(parent context.Context, j workerplan.Job, input io.Reader) (workerplan.Result, error) {
	if j.Validate() != nil || runtime.Version() != j.Runtime {
		return workerplan.Result{}, workerplan.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(j.Resources.WallSeconds)*time.Second)
	defer cancel()
	r := workerplan.Result{Schema: workerplan.ResultSchema, JobID: j.ID, JobSHA256: j.SHA256, Status: "failed", EnforcementScope: "artifact_bounds_and_cooperative_context_only", Authority: "synthetic_worker_protocol_only"}
	finish := func() (workerplan.Result, error) {
		r.SHA256 = ""
		r.SHA256 = workerplan.Hash(r)
		raw, _ := json.Marshal(r)
		if len(raw) > workerplan.MaxResultBytes {
			r.Status, r.ErrorCode, r.NativeResult, r.NativeResultSHA256, r.NativeResultBytes = "failed", "output_limit_exceeded", "", "", 0
			r.SHA256 = ""
			r.SHA256 = workerplan.Hash(r)
		}
		return r, r.Validate(j)
	}
	if ctx.Err() != nil {
		r.ErrorCode = "cancelled_before_input"
		return finish()
	}
	raw, err := io.ReadAll(io.LimitReader(input, int64(j.Resources.MaxInputBytes)+1))
	if err != nil || len(raw) != j.Input.Bytes || len(raw) > workerplan.MaxInputBytes || workerplan.Digest(raw) != j.Input.SHA256 {
		return workerplan.Result{}, workerplan.ErrInvalid
	}
	r.InputVerified = true
	var dataset replay.Dataset
	if workerplan.ReadDatasetShape(raw, &dataset) != nil {
		r.ErrorCode = "input_decode_failed"
		r.DeadlineMet = ctx.Err() == nil
		return finish()
	}
	canonical, err := json.Marshal(dataset)
	if err != nil {
		return workerplan.Result{}, err
	}
	if workerplan.Digest(canonical) != j.Input.CanonicalSHA256 {
		r.ErrorCode = "canonical_input_mismatch"
		r.DeadlineMet = ctx.Err() == nil
		return finish()
	}
	native, err := experiment.Execute(ctx, workerplan.NativeAttemptID(j), j.Recipe, dataset)
	if err != nil {
		return workerplan.Result{}, err
	}
	artifact, err := json.Marshal(native)
	if err != nil {
		return workerplan.Result{}, err
	}
	r.DeadlineMet = ctx.Err() == nil
	if len(artifact) > workerplan.MaxNativeBytes {
		r.ErrorCode = "output_limit_exceeded"
		return finish()
	}
	r.Status, r.ErrorCode = native.Status, native.ErrorCode
	r.NativeResult = string(artifact)
	r.NativeResultBytes = len(artifact)
	r.NativeResultSHA256 = workerplan.Digest(artifact)
	return finish()
}
