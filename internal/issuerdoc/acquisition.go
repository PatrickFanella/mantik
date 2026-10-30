package issuerdoc

import (
	"net/http"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const AcquisitionSchema = "mantik-issuer-document-acquisition-v1"

// Acquisition binds one retained document for service custody. Both journal
// receipt hashes are recomputable from these fields, so custody can verify the
// exact original bytes without access to the journal.
type Acquisition struct {
	Schema           string    `json:"schema"`
	JournalID        string    `json:"journal_id"`
	Spec             Spec      `json:"spec"`
	URL              string    `json:"url"`
	RequestedAt      time.Time `json:"requested_at"`
	IntentSHA256     string    `json:"intent_sha256"`
	CompletedAt      time.Time `json:"completed_at"`
	ContentType      string    `json:"content_type"`
	DocumentSHA256   string    `json:"document_sha256"`
	DocumentBytes    int       `json:"document_bytes"`
	CompletionSHA256 string    `json:"completion_sha256"`
	SHA256           string    `json:"sha256"`
}

func (a Acquisition) seal() string { a.SHA256 = ""; return workerplan.Hash(a) }

// Acquisition exists only for a retained document; failed and interrupted
// collections have no custody binding.
func (s Status) Acquisition() (Acquisition, error) {
	if s.State != "retained" {
		return Acquisition{}, ErrCollectionUnresolved
	}
	a := Acquisition{Schema: AcquisitionSchema, JournalID: s.JournalID, Spec: s.Spec, URL: s.URL, RequestedAt: s.RequestedAt, IntentSHA256: s.IntentSHA256, CompletedAt: s.CompletedAt, ContentType: s.ContentType, DocumentSHA256: s.DocumentSHA256, DocumentBytes: s.DocumentBytes, CompletionSHA256: s.CompletionSHA256}
	a.SHA256 = a.seal()
	return a, nil
}

// Check recomputes the intent and completion receipts from the binding. It
// does not read the document; Verify additionally binds the exact bytes.
func (a Acquisition) Check() error {
	if a.Schema != AcquisitionSchema || !strings.HasPrefix(a.JournalID, "issuer-journal-") || a.Spec.Validate() != nil || a.URL != SPDRWorkbookURL || a.RequestedAt.IsZero() || a.CompletedAt.Before(a.RequestedAt) || a.DocumentBytes < 1 || a.DocumentBytes > MaxDocumentBytes || a.seal() != a.SHA256 {
		return ErrJournalConflict
	}
	in := intent{Schema: "mantik-issuer-document-intent-v1", JournalID: a.JournalID, Spec: a.Spec, URL: a.URL, RequestedAt: a.RequestedAt}
	done := completion{Schema: "mantik-issuer-document-completion-v1", IntentSHA256: a.IntentSHA256, CompletedAt: a.CompletedAt, HTTPStatus: http.StatusOK, ContentType: a.ContentType, FinalURL: a.URL, DocumentSHA256: a.DocumentSHA256, DocumentBytes: a.DocumentBytes}
	if in.seal() != a.IntentSHA256 || done.seal() != a.CompletionSHA256 {
		return ErrJournalConflict
	}
	return nil
}

// Verify binds the exact original bytes and rederives the parse. Receipt or
// byte mismatches return ErrJournalConflict; intact bytes that do not parse
// return ErrUnparseable.
func (a Acquisition) Verify(raw []byte) (Document, error) {
	if err := a.Check(); err != nil || len(raw) != a.DocumentBytes || workerplan.Digest(raw) != a.DocumentSHA256 {
		return Document{}, ErrJournalConflict
	}
	return ParseSPDRWorkbook(raw, a.Spec.Symbol, a.Spec.CUSIP)
}

// Retained returns a retained document's binding and exact original bytes.
func (j *Journal) Retained(id string) (Status, Acquisition, []byte, error) {
	s, raw, err := j.Document(id)
	if err != nil {
		return Status{}, Acquisition{}, nil, err
	}
	a, err := s.Acquisition()
	if err != nil {
		return Status{}, Acquisition{}, nil, err
	}
	return s, a, raw, nil
}
