package feedcollector

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

const MaxAdmissions = 2048

type Fence struct {
	RegistrationID string `json:"registration_id"`
	OwnerID        string `json:"owner_id"`
	Generation     uint64 `json:"generation"`
}

type Admission struct {
	Fence                Fence                 `json:"fence"`
	InputSHA256          string                `json:"input_sha256"`
	At                   time.Time             `json:"at"`
	Receipt              domain.RoutingReceipt `json:"receipt"`
	InputAlreadyRetained bool                  `json:"input_already_retained"`
}

var captureLeaf = regexp.MustCompile(`^capture-[a-zA-Z0-9_-]{1,40}-r[1-9][0-9]{0,6}\.json$`)

func ValidateFence(f Fence) error {
	if !idPattern.MatchString(f.RegistrationID) || !idPattern.MatchString(f.OwnerID) || f.Generation == 0 {
		return ErrInvalid
	}
	return nil
}

func InputDigest(i domain.RoutingInput) string {
	raw, _ := json.Marshal(i)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// CheckSubmission grants either current admission authority or an exact
// historical replay. The storage transaction must verify retained input/receipt
// integrity before returning a historical receipt.
func CheckSubmission(s State, f Fence, i domain.RoutingInput, at time.Time) (bool, error) {
	if err := ValidateFence(f); err != nil {
		return false, err
	}
	e, ok := s.Entries[f.RegistrationID]
	if !ok {
		return false, ErrNotFound
	}
	r := e.Registration
	p := i.Provenance
	source := "alpaca-" + strings.ReplaceAll(r.Feed, "_", "-")
	if p == nil || i.Symbol != r.Symbol || i.EvidenceClass != r.EvidenceClass || p.SourceSymbol != r.Symbol || p.AccessRef != r.AccessRef || p.MappingVersion != "alpaca-us-equities-v1" || (i.Source != source && i.Source != "alpaca-regular-session") {
		return false, ErrInvalid
	}
	prefix := "journal:" + r.JournalID + ":"
	if !strings.HasPrefix(p.RawRef, prefix) || !captureLeaf.MatchString(strings.TrimPrefix(p.RawRef, prefix)) {
		return false, ErrInvalid
	}
	if old, exists := s.Admissions[i.ID]; exists {
		if old.InputSHA256 != InputDigest(i) || old.Fence.RegistrationID != f.RegistrationID {
			return false, ErrConflict
		}
		if old.Fence == f {
			return true, nil
		}
	}
	if at.IsZero() || at.Year() < 2000 || at.Year() > 9998 {
		return false, ErrInvalid
	}
	if i.ReceivedAt.After(at) {
		return false, ErrInvalid
	}
	if at.Before(s.LastClock) {
		return false, ErrClockBackwards
	}
	if e.Lease == nil || e.Lease.Stopped || e.Lease.OwnerID != f.OwnerID || e.Lease.Generation != f.Generation || !at.Before(e.Lease.ExpiresAt) {
		return false, ErrLeaseLost
	}
	if _, exists := s.Admissions[i.ID]; !exists && len(s.Admissions) >= MaxAdmissions {
		return false, ErrCapacity
	}
	return false, nil
}

func RetainAdmission(s State, f Fence, i domain.RoutingInput, receipt domain.RoutingReceipt, at time.Time) (State, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	var next State
	if err := json.Unmarshal(raw, &next); err != nil {
		return s, err
	}
	if next.Admissions == nil {
		next.Admissions = map[string]Admission{}
	}
	if _, exists := next.Admissions[i.ID]; !exists {
		alreadyRetained := receipt.Duplicate
		receipt.Duplicate = false
		next.Admissions[i.ID] = Admission{Fence: f, InputSHA256: InputDigest(i), At: at.UTC(), Receipt: receipt, InputAlreadyRetained: alreadyRetained}
	}
	next.LastClock = at.UTC()
	return next, nil
}
