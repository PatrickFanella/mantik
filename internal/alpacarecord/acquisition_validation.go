package alpacarecord

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Validate checks a retained receipt's envelope and exact recording binding.
// Actual acquisition artifacts are verified by the configured journal owner;
// hashes supplied by a remote caller do not establish that ownership.
func (a CorporateActionAcquisition) Validate(r Recording) error {
	invalid := errors.New("invalid corporate acquisition recording binding")
	if _, err := NormalizeCorporate(r); err != nil {
		return invalid
	}
	hash := ValidSHA256
	if a.Schema != "mantik-journal-corporate-acquisition-v1" || a.Method != "retained-intent-attempts-capture-completion-v1" || a.Authority != "journal_owned_recorded_acquisition_only" || !capturePattern.MatchString(a.JournalID) || !strings.HasPrefix(a.JournalID, "journal-") || !capturePattern.MatchString(a.CollectionID) || a.CollectionID != r.CaptureID || r.Revision != 1 || r.EvidenceClass != "prospective_market_unqualified" || a.CaptureRef != r.RawRef || a.CaptureRef != "journal:"+a.JournalID+":"+fmt.Sprintf("capture-%s-r1.json", a.CollectionID) || a.ProviderAttested || a.CoverageComplete || a.EconomicApplicationReady || a.AdmissionAvailable || a.ExecutionAvailable || a.QualificationCredit || !validTime(a.RequestedAt) || !validTime(a.ResponseAt) || !validTime(a.CompletedAt) || !a.ResponseAt.Equal(r.ReceivedAt) || !a.AvailableAt.Equal(a.CompletedAt) || a.CompletedAt.Before(a.ResponseAt) || len(a.Attempts) < 1 || len(a.Attempts) > 3 {
		return invalid
	}
	for _, s := range []string{a.IntentSHA256, a.CompletionSHA256, a.CaptureSHA256, a.RecordingSHA256, a.SourceSHA256, a.SHA256} {
		if !hash(s) {
			return invalid
		}
	}
	raw, err := json.Marshal(r)
	if err != nil || Digest(string(raw)) != a.RecordingSHA256 || a.SourceSHA256 != r.SHA256 {
		return invalid
	}
	clock := a.RequestedAt
	for i, v := range a.Attempts {
		if v.Number != i+1 || !hash(v.StartSHA256) || !hash(v.ResponseSHA256) || !validTime(v.StartedAt) || !validTime(v.EndedAt) || v.StartedAt.Before(clock) || v.EndedAt.Before(v.StartedAt) {
			return invalid
		}
		if v.State == "transport_failed" {
			if v.HTTPStatus != 0 || v.Partial {
				return invalid
			}
		} else if v.State != "response" || v.HTTPStatus < 100 || v.HTTPStatus > 599 {
			return invalid
		}
		clock = v.EndedAt
	}
	last := a.Attempts[len(a.Attempts)-1]
	if last.State != "response" || last.HTTPStatus != 200 || last.Partial || !last.EndedAt.Equal(a.ResponseAt) {
		return invalid
	}
	expected := a.SHA256
	a.SHA256 = ""
	raw, err = json.Marshal(a)
	if err != nil || Digest(string(raw)) != expected {
		return invalid
	}
	return nil
}

func ValidSHA256(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && strings.ToLower(s) == s
}
