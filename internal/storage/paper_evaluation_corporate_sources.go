package storage

import (
	"context"
	"encoding/json"
	"reflect"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// This binds service custody at assessment issuance, not the provider's earlier
// knowledge, completeness, relevance to a symbol, or economic interpretation.
type PaperEvaluationCorporateSourcePin struct {
	ID           string    `json:"id"`
	SHA256       string    `json:"sha256"`
	SourceSHA256 string    `json:"source_sha256"`
	Sequence     int       `json:"sequence"`
	RetainedAt   time.Time `json:"retained_at"`
}
type PaperEvaluationCorporateSourceCut struct {
	Schema                   string                              `json:"schema"`
	Method                   string                              `json:"method"`
	ReceiverID               string                              `json:"receiver_id"`
	DeclarationID            string                              `json:"declaration_id"`
	DeclarationSHA256        string                              `json:"declaration_sha256"`
	AsOf                     time.Time                           `json:"as_of"`
	Sources                  []PaperEvaluationCorporateSourcePin `json:"sources"`
	Authority                string                              `json:"authority"`
	ProviderAttested         bool                                `json:"provider_attested"`
	CoverageComplete         bool                                `json:"coverage_complete"`
	EconomicApplicationReady bool                                `json:"economic_application_ready"`
	AdmissionAvailable       bool                                `json:"admission_available"`
	ExecutionAvailable       bool                                `json:"execution_available"`
	QualificationCredit      bool                                `json:"qualification_credit"`
	SHA256                   string                              `json:"sha256"`
}

func corporateEvaluationCutFromPins(e PaperEvaluationEvidence, pins []PaperEvaluationCorporateSourcePin) (PaperEvaluationCorporateSourceCut, error) {
	if pins == nil || len(pins) > MaxCorporateActionSources || !declarationTime(e.AsOf) {
		return PaperEvaluationCorporateSourceCut{}, ErrPaperEvaluationAssessmentInvalid
	}
	ids := map[string]bool{}
	var clock time.Time
	for i, p := range pins {
		if !paperaccount.ID.MatchString(p.ID) || !decisionPin.MatchString(p.SHA256) || !decisionPin.MatchString(p.SourceSHA256) || p.Sequence != i+1 || !declarationTime(p.RetainedAt) || p.RetainedAt.After(e.AsOf) || p.RetainedAt.Before(clock) || ids[p.ID] {
			return PaperEvaluationCorporateSourceCut{}, ErrPaperEvaluationAssessmentInvalid
		}
		ids[p.ID] = true
		clock = p.RetainedAt
	}
	r := PaperEvaluationCorporateSourceCut{Schema: "mantik-owned-evaluation-corporate-source-cut-v1", Method: "service-custody-prefix-at-assessment-v1", ReceiverID: e.ReceiverID, DeclarationID: e.Declaration.Command.ID, DeclarationSHA256: e.Declaration.SHA256, AsOf: e.AsOf, Sources: clone(pins), Authority: "service_owned_reported_source_custody_cut_only"}
	r.SHA256 = workerplan.Hash(r)
	return r, nil
}
func ownedCorporateEvaluationCut(ctx context.Context, e PaperEvaluationEvidence, records []CorporateActionSourceRecord) (PaperEvaluationCorporateSourceCut, error) {
	cat, err := corporateActionSourceCatalog(ctx, e.ReceiverID, records)
	if err != nil {
		return PaperEvaluationCorporateSourceCut{}, err
	}
	pins := []PaperEvaluationCorporateSourcePin{}
	for _, r := range cat.Records {
		if !r.RetainedAt.After(e.AsOf) {
			pins = append(pins, PaperEvaluationCorporateSourcePin{r.Command.ID, r.SHA256, r.Command.Recording.SHA256, r.Sequence, r.RetainedAt})
		}
	}
	return corporateEvaluationCutFromPins(e, pins)
}

// Envelope replay is used only as a clock floor. Actual catalogue replay builds
// the expected cut again from the actual source owner in the same snapshot.
func paperEvaluationAssessmentWithSources(c PaperEvaluationAssessmentCommand, e PaperEvaluationEvidence, w *PaperEvaluationWarmUp, cut *PaperEvaluationCorporateSourceCut, schema string) (PaperEvaluationAssessment, error) {
	if schema != "mantik-owned-paper-evaluation-assessment-v5" {
		if cut != nil {
			return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
		}
		return paperEvaluationAssessmentRecipe(c, e, w, schema)
	}
	r, err := paperEvaluationAssessmentRecipe(c, e, w, "mantik-owned-paper-evaluation-assessment-v4")
	if err != nil || cut == nil {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	want, err := corporateEvaluationCutFromPins(e, cut.Sources)
	if err != nil || !reflect.DeepEqual(want, *cut) {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentInvalid
	}
	r.Schema = schema
	r.Recipe = "native-predeclared-necessary-checks-v5"
	value := clone(want)
	r.CorporateSourceCut = &value
	r.SHA256 = ""
	r.SHA256 = workerplan.Hash(r)
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxPaperEvaluationAssessmentBytes {
		return PaperEvaluationAssessment{}, ErrPaperEvaluationAssessmentCapacity
	}
	return r, nil
}
