package extraction

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"
)

const SwitchyardCaptureSchema = "mantik-switchyard-extraction-capture-v1"
const SwitchyardCaptureHistorySchema = "mantik-switchyard-extraction-capture-history-v1"
const SwitchyardCaptureAuthority = "caller_reported_switchyard_capture_only"
const MaxSwitchyardCaptureBytes = 2*MaxLocalReceiptBytes + (64 << 10)

// SwitchyardCapture is an immutable import, not a service-owned execution intent.
// A prepared-only import remains unknown even if a later capture is imported.
type SwitchyardCapture struct {
	Schema              string               `json:"schema"`
	ReceiverID          string               `json:"receiver_id"`
	ID                  string               `json:"id"`
	Mode                string               `json:"mode"`
	ImportedAt          time.Time            `json:"imported_at"`
	Prepared            SwitchyardReceipt    `json:"prepared"`
	Finished            *SwitchyardReceipt   `json:"finished,omitempty"`
	Inspection          SwitchyardInspection `json:"inspection"`
	Authority           string               `json:"authority"`
	ExecutionAttested   bool                 `json:"execution_attested"`
	QualificationCredit bool                 `json:"qualification_credit"`
	SHA256              string               `json:"sha256"`
}

func RetainSwitchyard(ctx context.Context, receiver, id string, p SwitchyardReceipt, f *SwitchyardReceipt, at time.Time) (SwitchyardCapture, error) {
	if err := ctx.Err(); err != nil {
		return SwitchyardCapture{}, err
	}
	if !receiverID.MatchString(receiver) || !ID.MatchString(id) || !validAt(at) {
		return SwitchyardCapture{}, ErrInvalid
	}
	// Bound the whole receipt structures too, including unexpected large metadata.
	for _, v := range []any{p, f} {
		b, err := json.Marshal(v)
		if err != nil || len(b) > MaxLocalReceiptBytes {
			return SwitchyardCapture{}, ErrInvalid
		}
	}
	p = copyJSON(p)
	if f != nil {
		detached := copyJSON(*f)
		f = &detached
	}
	report, err := InspectSwitchyard(ctx, p, f)
	if err != nil {
		return SwitchyardCapture{}, err
	}
	r := SwitchyardCapture{Schema: SwitchyardCaptureSchema, ReceiverID: receiver, ID: id, Mode: "caller_reported_switchyard_capture", ImportedAt: at.UTC(), Prepared: p, Finished: f, Inspection: report, Authority: SwitchyardCaptureAuthority}
	r.SHA256 = hash(r)
	if err := ctx.Err(); err != nil {
		return SwitchyardCapture{}, err
	}
	return r, nil
}

func (r SwitchyardCapture) ValidateContext(ctx context.Context) error {
	expected, err := RetainSwitchyard(ctx, r.ReceiverID, r.ID, r.Prepared, r.Finished, r.ImportedAt)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, r) {
		return ErrInvalid
	}
	return nil
}
func (r SwitchyardCapture) Validate() error { return r.ValidateContext(context.Background()) }
func SameSwitchyardCapture(r SwitchyardCapture, p SwitchyardReceipt, f *SwitchyardReceipt) bool {
	return reflect.DeepEqual(r.Prepared, p) && reflect.DeepEqual(r.Finished, f)
}

type SwitchyardCaptureSummary struct {
	ID            string               `json:"id"`
	CaptureSHA256 string               `json:"capture_sha256"`
	ImportedAt    time.Time            `json:"imported_at"`
	Inspection    SwitchyardInspection `json:"inspection"`
}
type SwitchyardCaptureHistory struct {
	Schema              string                     `json:"schema"`
	ReceiverID          string                     `json:"receiver_id"`
	Captures            []SwitchyardCaptureSummary `json:"captures"`
	CaptureCount        int                        `json:"capture_count"`
	StateCounts         map[string]int             `json:"state_counts"`
	Authority           string                     `json:"authority"`
	ExecutionAttested   bool                       `json:"execution_attested"`
	QualificationCredit bool                       `json:"qualification_credit"`
	SHA256              string                     `json:"sha256"`
}

func MakeSwitchyardCaptureHistory(ctx context.Context, receiver string, all []SwitchyardCapture) (SwitchyardCaptureHistory, error) {
	if !receiverID.MatchString(receiver) || len(all) > MaxAttempts {
		return SwitchyardCaptureHistory{}, ErrInvalid
	}
	h := SwitchyardCaptureHistory{Schema: SwitchyardCaptureHistorySchema, ReceiverID: receiver, Captures: []SwitchyardCaptureSummary{}, StateCounts: map[string]int{"prepared_only": 0, "finished": 0}, Authority: SwitchyardCaptureAuthority}
	for _, r := range all {
		if r.ReceiverID != receiver {
			return SwitchyardCaptureHistory{}, ErrInvalid
		}
		if err := r.ValidateContext(ctx); err != nil {
			return SwitchyardCaptureHistory{}, err
		}
		h.Captures = append(h.Captures, SwitchyardCaptureSummary{r.ID, r.SHA256, r.ImportedAt, r.Inspection})
		h.StateCounts[r.Inspection.State]++
	}
	slices.SortFunc(h.Captures, func(a, b SwitchyardCaptureSummary) int { return strings.Compare(a.ID, b.ID) })
	h.CaptureCount = len(h.Captures)
	h.SHA256 = hash(h)
	return h, h.Validate()
}

// Validate checks a compact response's shape and hashes. Full replay requires
// the targeted capture; MakeSwitchyardCaptureHistory replays every retained row.
func (h SwitchyardCaptureHistory) Validate() error {
	if h.Schema != SwitchyardCaptureHistorySchema || !receiverID.MatchString(h.ReceiverID) || h.Authority != SwitchyardCaptureAuthority || h.ExecutionAttested || h.QualificationCredit || h.Captures == nil || len(h.Captures) > MaxAttempts || len(h.Captures) != h.CaptureCount {
		return ErrInvalid
	}
	counts := map[string]int{"prepared_only": 0, "finished": 0}
	for i, s := range h.Captures {
		r := s.Inspection
		digest := r.SHA256
		r.SHA256 = ""
		if !ID.MatchString(s.ID) || i > 0 && h.Captures[i-1].ID >= s.ID || !validAt(s.ImportedAt) || !sha.MatchString(s.CaptureSHA256) || digest != hash(r) || r.Schema != SwitchyardInspectionSchema || r.Authority != "retained_switchyard_bytes_consistency_only" || r.ExecutionAttested || r.QualificationCredit || SwitchyardConfig(r.Config).Validate() != nil || r.PromptVersion != LocalPromptVersion || !sha.MatchString(r.ArticleSHA256) || !sha.MatchString(r.ContractSHA256) || !sha.MatchString(r.PreparedSHA256) || !eventKey.MatchString(r.EventKey) || r.Checks == nil {
			return ErrInvalid
		}
		if _, ok := counts[r.State]; !ok {
			return ErrInvalid
		}
		if r.State == "prepared_only" && (r.Disposition != "completion_unknown" || r.FinishedSHA256 != "" || r.Outcome != "" || r.ReasonCode != "" || r.ClaimCount != 0 || r.ModelArtifactSHA256 != "" || r.CandidateArtifactSHA256 != "") {
			return ErrInvalid
		}
		if r.State == "finished" && (!sha.MatchString(r.FinishedSHA256) || !slices.Contains([]string{"extracted", "abstain"}, r.Outcome) || r.ReasonCode == "" || r.Disposition == "" || r.ClaimCount < 0 || r.Outcome == "abstain" && r.ClaimCount != 0 || r.Outcome == "extracted" && r.ClaimCount == 0) {
			return ErrInvalid
		}
		counts[r.State]++
	}
	digest := h.SHA256
	h.SHA256 = ""
	if !reflect.DeepEqual(counts, h.StateCounts) || digest != hash(h) {
		return ErrInvalid
	}
	return nil
}
