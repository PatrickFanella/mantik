// Package feedcollector retains declared feed ownership independently of
// research inputs, fixture execution, and provider credentials.
package feedcollector

import (
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

const MaxRegistrations = 16
const MaxCommands = 4096

var (
	ErrInvalid        = errors.New("invalid feed collector command")
	ErrConflict       = errors.New("feed collector command conflict")
	ErrLeaseLost      = errors.New("feed collector lease lost")
	ErrNotFound       = errors.New("feed registration not found")
	ErrClockBackwards = errors.New("feed collector clock moved backwards")
	ErrCapacity       = errors.New("feed collector history capacity exhausted")
	idPattern         = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
	symbolPattern     = regexp.MustCompile(`^[A-Z][A-Z0-9.]{0,14}$`)
)

// Registration is immutable. AccessRef identifies a private access receipt;
// credential values and paths are not part of the service contract.
type Registration struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Feed          string `json:"feed"`
	Symbol        string `json:"symbol"`
	EvidenceClass string `json:"evidence_class"`
	JournalID     string `json:"journal_id"`
	AccessRef     string `json:"access_ref"`
	LeaseSeconds  int    `json:"lease_seconds"`
}

type Lease struct {
	OwnerID    string    `json:"owner_id"`
	Generation uint64    `json:"generation"`
	ClaimedAt  time.Time `json:"claimed_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Stopped    bool      `json:"stopped"`
}

type Entry struct {
	Registration Registration `json:"registration"`
	RegisteredAt time.Time    `json:"registered_at"`
	Lease        *Lease       `json:"lease,omitempty"`
}

// RequestID identifies one immutable command, including renewals. An exact
// retry returns its historical receipt and never extends or revives a lease.
type Command struct {
	RequestID      string        `json:"request_id"`
	Operation      string        `json:"operation"`
	RegistrationID string        `json:"registration_id"`
	Registration   *Registration `json:"registration,omitempty"`
	OwnerID        string        `json:"owner_id,omitempty"`
	Generation     uint64        `json:"generation,omitempty"`
}

type Receipt struct {
	RequestID string    `json:"request_id"`
	Operation string    `json:"operation"`
	At        time.Time `json:"at"`
	Entry     Entry     `json:"entry"`
}

type RetainedCommand struct {
	Command Command `json:"command"`
	Receipt Receipt `json:"receipt"`
}

type State struct {
	LastClock  time.Time                  `json:"last_clock"`
	Entries    map[string]Entry           `json:"entries"`
	Commands   map[string]RetainedCommand `json:"commands"`
	Admissions map[string]Admission       `json:"admissions"`
}

func InitialState() State {
	return State{Entries: map[string]Entry{}, Commands: map[string]RetainedCommand{}, Admissions: map[string]Admission{}}
}

func Validate(c Command) error {
	if !idPattern.MatchString(c.RequestID) || !idPattern.MatchString(c.RegistrationID) {
		return ErrInvalid
	}
	switch c.Operation {
	case "register":
		if c.Registration == nil || c.OwnerID != "" || c.Generation != 0 {
			return ErrInvalid
		}
		r := *c.Registration
		if r.ID != c.RegistrationID || r.Provider != "alpaca" ||
			(r.Feed != "iex" && r.Feed != "sip" && r.Feed != "delayed_sip") ||
			!symbolPattern.MatchString(r.Symbol) || r.EvidenceClass != "prospective_market_unqualified" ||
			!idPattern.MatchString(r.JournalID) || !idPattern.MatchString(r.AccessRef) ||
			r.LeaseSeconds < 5 || r.LeaseSeconds > 300 {
			return ErrInvalid
		}
	case "claim":
		if c.Registration != nil || !idPattern.MatchString(c.OwnerID) || c.Generation != 0 {
			return ErrInvalid
		}
	case "renew", "stop":
		if c.Registration != nil || !idPattern.MatchString(c.OwnerID) || c.Generation == 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// Apply copies state for a new accepted command; retries and rejected commands
// leave the original state untouched. Returned receipts have detached leases.
func Apply(s State, c Command, at time.Time) (State, Receipt, error) {
	if err := Validate(c); err != nil {
		return s, Receipt{}, err
	}
	if old, ok := s.Commands[c.RequestID]; ok {
		if !sameCommand(old.Command, c) {
			return s, Receipt{}, ErrConflict
		}
		receipt := old.Receipt
		receipt.Entry = detachedEntry(receipt.Entry)
		return s, receipt, nil
	}
	if at.IsZero() || at.Year() < 2000 || at.Year() > 9998 {
		return s, Receipt{}, ErrInvalid
	}
	at = at.UTC()
	if at.Before(s.LastClock) {
		return s, Receipt{}, ErrClockBackwards
	}
	if len(s.Commands) >= MaxCommands {
		return s, Receipt{}, ErrCapacity
	}
	e, exists := s.Entries[c.RegistrationID]
	switch c.Operation {
	case "register":
		if exists {
			if e.Registration != *c.Registration {
				return s, Receipt{}, ErrConflict
			}
		} else {
			if len(s.Entries) >= MaxRegistrations {
				return s, Receipt{}, ErrCapacity
			}
			for _, registered := range s.Entries {
				r := registered.Registration
				if r.JournalID == c.Registration.JournalID || (r.Provider == c.Registration.Provider && r.Feed == c.Registration.Feed && r.Symbol == c.Registration.Symbol && r.EvidenceClass == c.Registration.EvidenceClass) {
					return s, Receipt{}, ErrConflict
				}
			}
			e = Entry{Registration: *c.Registration, RegisteredAt: at}
		}
	case "claim":
		if !exists {
			return s, Receipt{}, ErrNotFound
		}
		var generation uint64 = 1
		if e.Lease != nil {
			if !e.Lease.Stopped && at.Before(e.Lease.ExpiresAt) {
				return s, Receipt{}, ErrConflict
			}
			generation = e.Lease.Generation + 1
		}
		e.Lease = &Lease{OwnerID: c.OwnerID, Generation: generation, ClaimedAt: at, UpdatedAt: at, ExpiresAt: at.Add(time.Duration(e.Registration.LeaseSeconds) * time.Second)}
	case "renew", "stop":
		if !exists {
			return s, Receipt{}, ErrNotFound
		}
		if e.Lease == nil || e.Lease.Stopped || e.Lease.OwnerID != c.OwnerID || e.Lease.Generation != c.Generation || (c.Operation == "renew" && !at.Before(e.Lease.ExpiresAt)) {
			return s, Receipt{}, ErrLeaseLost
		}
		lease := *e.Lease
		lease.UpdatedAt = at
		if c.Operation == "stop" {
			lease.Stopped = true
		} else {
			lease.ExpiresAt = at.Add(time.Duration(e.Registration.LeaseSeconds) * time.Second)
		}
		e.Lease = &lease
	}
	// JSON detachment also removes aliases held by callers of Apply.
	raw, err := json.Marshal(s)
	if err != nil {
		return s, Receipt{}, err
	}
	var next State
	if err := json.Unmarshal(raw, &next); err != nil {
		return s, Receipt{}, err
	}
	if next.Entries == nil {
		next.Entries = map[string]Entry{}
	}
	if next.Commands == nil {
		next.Commands = map[string]RetainedCommand{}
	}
	next.LastClock = at
	e = detachedEntry(e)
	next.Entries[c.RegistrationID] = e
	receipt := Receipt{RequestID: c.RequestID, Operation: c.Operation, At: at, Entry: e}
	// Detach command's optional registration from the caller as well.
	if c.Registration != nil {
		registration := *c.Registration
		c.Registration = &registration
	}
	next.Commands[c.RequestID] = RetainedCommand{Command: c, Receipt: receipt}
	receipt.Entry = detachedEntry(receipt.Entry)
	return next, receipt, nil
}

func detachedEntry(e Entry) Entry {
	if e.Lease != nil {
		lease := *e.Lease
		e.Lease = &lease
	}
	return e
}

func sameCommand(a, b Command) bool {
	if a.RequestID != b.RequestID || a.Operation != b.Operation || a.RegistrationID != b.RegistrationID || a.OwnerID != b.OwnerID || a.Generation != b.Generation {
		return false
	}
	if a.Registration == nil || b.Registration == nil {
		return a.Registration == nil && b.Registration == nil
	}
	return *a.Registration == *b.Registration
}

// LeaseStatus describes retained declarations, not actual process liveness.
func LeaseStatus(e Entry, lastClock, at time.Time) string {
	if at.IsZero() || at.Before(lastClock) {
		return "clock_uncertain"
	}
	if e.Lease == nil {
		return "unclaimed"
	}
	if e.Lease.Stopped {
		return "stopped"
	}
	if !at.Before(e.Lease.ExpiresAt) {
		return "expired"
	}
	return "lease_current"
}
