package polygonref

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const AcquisitionSchema = "mantik-polygon-reference-acquisition-v1"

// Acquisition binds one retained collection for service custody. Every journal
// receipt hash is recomputable from these fields, so custody can verify the
// chain and the exact page bodies without access to the journal.
type Acquisition struct {
	Schema           string       `json:"schema"`
	JournalID        string       `json:"journal_id"`
	Spec             Spec         `json:"spec"`
	RequestedAt      time.Time    `json:"requested_at"`
	IntentSHA256     string       `json:"intent_sha256"`
	Pages            []PageStatus `json:"pages"`
	CompletedAt      time.Time    `json:"completed_at"`
	CompletionSHA256 string       `json:"completion_sha256"`
	SHA256           string       `json:"sha256"`
}

func (a Acquisition) seal() string { a.SHA256 = ""; return workerplan.Hash(a) }

// Acquisition exists only for a retained collection; failed and interrupted
// collections have no custody binding.
func (s Status) Acquisition() (Acquisition, error) {
	if s.State != "retained" {
		return Acquisition{}, ErrCollectionUnresolved
	}
	a := Acquisition{Schema: AcquisitionSchema, JournalID: s.JournalID, Spec: s.Spec, RequestedAt: s.RequestedAt, IntentSHA256: s.IntentSHA256, Pages: slices.Clone(s.Pages), CompletedAt: s.CompletedAt, CompletionSHA256: s.CompletionSHA256}
	a.SHA256 = a.seal()
	return a, nil
}

// Check recomputes the intent, page and completion receipts from the binding.
// It does not read bodies; Verify additionally binds the exact bytes.
func (a Acquisition) Check() error {
	if a.Schema != AcquisitionSchema || !strings.HasPrefix(a.JournalID, "polygon-journal-") || a.Spec.Validate() != nil || a.RequestedAt.IsZero() || len(a.Pages) == 0 || len(a.Pages) > MaxPages || a.seal() != a.SHA256 {
		return ErrJournalConflict
	}
	in := intent{Schema: "mantik-polygon-reference-intent-v1", JournalID: a.JournalID, Spec: a.Spec, FirstURL: a.Spec.FirstURL(), RequestedAt: a.RequestedAt}
	if in.seal() != a.IntentSHA256 || a.Pages[0].RequestURL != in.FirstURL {
		return ErrJournalConflict
	}
	previous := ""
	for i, ps := range a.Pages {
		p := page{Schema: "mantik-polygon-reference-page-v1", IntentSHA256: a.IntentSHA256, PreviousSHA256: previous, Index: i + 1, RequestURL: ps.RequestURL, ReceivedAt: ps.ReceivedAt, HTTPStatus: http.StatusOK, BodySHA256: ps.BodySHA256, BodyBytes: ps.BodyBytes}
		if ps.Index != i+1 || ps.ReceivedAt.Before(a.RequestedAt) || ps.BodyBytes < 1 || ps.BodyBytes > MaxPageBytes || p.seal() != ps.SHA256 {
			return ErrJournalConflict
		}
		previous = ps.SHA256
	}
	last := a.Pages[len(a.Pages)-1]
	done := completion{Schema: "mantik-polygon-reference-completion-v1", IntentSHA256: a.IntentSHA256, LastPageSHA256: previous, Pages: len(a.Pages), CompletedAt: a.CompletedAt}
	if a.CompletedAt.Before(last.ReceivedAt) || done.seal() != a.CompletionSHA256 {
		return ErrJournalConflict
	}
	return nil
}

// Verify binds the exact page bodies to the receipts, including each cursor
// continuation, and rederives the snapshot. Receipt or byte mismatches return
// ErrJournalConflict; intact bytes that do not parse return ErrUnparseable.
func (a Acquisition) Verify(bodies [][]byte) (Snapshot, error) {
	if err := a.Check(); err != nil || len(bodies) != len(a.Pages) {
		return Snapshot{}, ErrJournalConflict
	}
	first := a.Pages[0].RequestURL
	next := first
	for i, ps := range a.Pages {
		if ps.RequestURL != next || len(bodies[i]) != ps.BodyBytes || workerplan.Digest(bodies[i]) != ps.BodySHA256 {
			return Snapshot{}, ErrJournalConflict
		}
		next = ""
		if env, err := decodePage(bodies[i]); err == nil && env.NextURL != "" {
			if n, err := NextURL(first, env.NextURL); err == nil {
				next = n
			}
		}
	}
	return Parse(a.Spec, bodies)
}

// Retained returns a retained collection's binding and exact page bodies.
func (j *Journal) Retained(id string) (Status, Acquisition, [][]byte, error) {
	s, err := j.Collection(id)
	if err != nil {
		return Status{}, Acquisition{}, nil, err
	}
	a, err := s.Acquisition()
	if err != nil {
		return Status{}, Acquisition{}, nil, err
	}
	bodies := make([][]byte, 0, len(s.Pages))
	for _, p := range s.Pages {
		body, err := j.Read(bodyName(id, p.Index), MaxPageBytes)
		if err != nil {
			return Status{}, Acquisition{}, nil, err
		}
		bodies = append(bodies, body)
	}
	return s, a, bodies, nil
}
