package socialwatch

import (
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// SourceVersion identifies exact retained source bytes, including the original
// capture time. A source identity alone cannot pin the version a repost saw.
type SourceVersion struct {
	CaptureID      string    `json:"capture_id"`
	CaptureSHA256  string    `json:"capture_sha256"`
	OriginalSHA256 string    `json:"original_sha256"`
	Source         SourceRef `json:"source"`
	Revision       int64     `json:"revision"`
	OccurredAt     time.Time `json:"occurred_at"`
	RetainedAt     time.Time `json:"retained_at"`
}

type Applicability struct {
	Schema                    string          `json:"schema"`
	ReceiverID                string          `json:"receiver_id"`
	CaptureID                 string          `json:"capture_id"`
	CaptureSHA256             string          `json:"capture_sha256"`
	HistorySHA256             string          `json:"history_sha256"`
	AvailableHistorySHA256    string          `json:"available_history_sha256"`
	AsOf                      time.Time       `json:"as_of"`
	Status                    string          `json:"status"`
	EvidenceClass             string          `json:"evidence_class"`
	Selected                  SourceVersion   `json:"selected"`
	Current                   *SourceVersion  `json:"current"`
	Lineage                   []SourceVersion `json:"lineage"`
	CurrentPolicySHA256       string          `json:"current_policy_sha256"`
	Authority                 string          `json:"authority"`
	ProviderAccessVerified    bool            `json:"provider_access_verified"`
	InstrumentMappingVerified bool            `json:"instrument_mapping_verified"`
	IndependentEvidenceCredit bool            `json:"independent_evidence_credit"`
	DispatchAvailable         bool            `json:"dispatch_available"`
	AdmissionAvailable        bool            `json:"admission_available"`
	ExecutionAvailable        bool            `json:"execution_available"`
	QualificationCredit       bool            `json:"qualification_credit"`
	SHA256                    string          `json:"sha256"`
}

func sourceVersion(c CaptureRecord) SourceVersion {
	return SourceVersion{c.Command.ID, c.SHA256, c.Command.OriginalSHA256, c.Source.Source, c.Source.Revision, c.Source.OccurredAt, c.RetainedAt}
}

// InspectApplicability validates the complete owner journal, then inspects only
// the prefix actually retained by the cutoff. It neither modifies capture
// receipts nor turns a source assertion into verified evidence. Callers of the
// HTTP endpoint cannot select the clock or import a replacement history.
func InspectApplicability(j Journal, id string, at time.Time) (Applicability, error) {
	if !validTime(at) {
		return Applicability{}, ErrInvalid
	}
	catalog, err := Read(j)
	if err != nil {
		return Applicability{}, err
	}
	return inspectApplicability(j, id, at, catalog.HeadSHA256)
}

// The caller has already validated every entry in this journal prefix. Keeping
// this inspection separate avoids recursive journal replay during issuance.
func inspectApplicability(j Journal, id string, at time.Time, head string) (Applicability, error) {
	var selected *CaptureRecord
	for _, e := range j.Entries {
		if e.Capture != nil && e.Capture.Command.ID == id {
			c := *e.Capture
			selected = &c
			break
		}
	}
	if selected == nil {
		return Applicability{}, ErrNotFound
	}
	out := Applicability{Schema: "mantik-social-source-applicability-v1", ReceiverID: j.ReceiverID, CaptureID: id, CaptureSHA256: selected.SHA256, HistorySHA256: head, AsOf: at.UTC(), EvidenceClass: selected.Command.EvidenceClass, Selected: sourceVersion(*selected), Lineage: []SourceVersion{}, Authority: "recorded_social_source_applicability_only"}
	finish := func(status string) (Applicability, error) {
		out.Status = status
		out.SHA256 = workerplan.Hash(out)
		return clone(out), nil
	}
	heads := map[string]CaptureRecord{}
	policies := map[string]PolicyRecord{}
	// Lineage is bound at the actual child capture, before later edits or
	// recoveries. Equal timestamps use retained journal sequence, never map order.
	lineages := map[string][]SourceVersion{}
	faults := map[string]string{}
	for _, e := range j.Entries {
		if e.At.After(at) {
			break
		}
		out.AvailableHistorySHA256 = e.SHA256
		if e.Policy != nil {
			policies[e.Policy.Command.Policy.ID] = *e.Policy
			continue
		}
		if e.Capture == nil {
			continue
		}
		c := *e.Capture
		if !c.SourceAccepted {
			continue
		}
		lineage := []SourceVersion{}
		if previous, ok := heads[sourceKey(c.Source.Source)]; ok {
			lineage = append(lineage, lineages[previous.SHA256]...)
			faults[c.SHA256] = faults[previous.SHA256]
		}
		if c.Source.Kind == "repost" {
			parent, ok := heads[sourceKey(*c.Source.RepostOf)]
			if ok {
				lineage = append(lineage, sourceVersion(parent))
				lineage = append(lineage, lineages[parent.SHA256]...)
			}
			switch {
			case !ok || parent.Source.Kind == "delete":
				faults[c.SHA256] = "repost_parent_unavailable"
			case parent.Source.OccurredAt.After(c.Source.OccurredAt):
				faults[c.SHA256] = "repost_parent_after_occurrence"
			case parent.Command.EvidenceClass != c.Command.EvidenceClass:
				faults[c.SHA256] = "repost_evidence_class_mismatch"
			case faults[parent.SHA256] != "":
				faults[c.SHA256] = faults[parent.SHA256]
			}
		}
		lineages[c.SHA256] = lineage
		heads[sourceKey(c.Source.Source)] = c
	}
	if selected.RetainedAt.After(at) {
		return finish("not_available_at_cutoff")
	}
	policy, ok := policies[selected.Command.WatchID]
	if !ok {
		return Applicability{}, ErrInvalid
	}
	out.CurrentPolicySHA256 = policy.SHA256
	current, exists := heads[sourceKey(selected.Source.Source)]
	if exists {
		v := sourceVersion(current)
		out.Current = &v
	}
	if !selected.SourceAccepted {
		return finish(selected.SourceStatus)
	}
	if !exists {
		return Applicability{}, ErrInvalid
	}
	out.Lineage = append(out.Lineage, lineages[selected.SHA256]...)
	if current.Source.Kind == "delete" {
		return finish("source_deleted")
	}
	if current.SHA256 != selected.SHA256 {
		return finish("source_superseded")
	}
	if fault := faults[selected.SHA256]; fault != "" {
		return finish(fault)
	}
	for _, ancestor := range out.Lineage {
		current, ok := heads[sourceKey(ancestor.Source)]
		if !ok || current.Source.Kind == "delete" {
			return finish("repost_parent_deleted")
		}
		if !reflect.DeepEqual(sourceVersion(current), ancestor) {
			return finish("repost_parent_changed")
		}
	}
	if policy.SHA256 != selected.Command.PolicySHA256 {
		return finish("watch_policy_changed")
	}
	if !policy.Command.Policy.Enabled {
		return finish("watch_disabled")
	}
	if at.Sub(selected.Source.OccurredAt) > time.Duration(policy.Command.Policy.MaxObservationDelaySeconds)*time.Second {
		return finish("source_stale")
	}
	if selected.PolicyStatus != "dispatch_unbound" {
		return finish(selected.PolicyStatus)
	}
	return finish("current_source_assertion_dispatch_unbound")
}
