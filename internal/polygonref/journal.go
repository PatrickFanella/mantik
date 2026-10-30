package polygonref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/privatejournal"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const journalSchema = "mantik-polygon-reference-journal-v1"
const maxReceiptBytes = 64 << 10
const fetchTimeout = 30 * time.Second

// Polygon's account quota is shared with other consumers of the same key.
// Pages are paced rather than retried; a 429 fails the collection.
const pageInterval = 13 * time.Second

var ErrJournalBusy = privatejournal.ErrBusy
var ErrJournalConflict = privatejournal.ErrConflict
var ErrCollectionUnresolved = errors.New("polygon reference collection has an interrupted or failed attempt; use a new collection identity")
var ErrCollectionNotFound = errors.New("polygon reference collection not found")

type intent struct {
	Schema      string    `json:"schema"`
	JournalID   string    `json:"journal_id"`
	Spec        Spec      `json:"spec"`
	FirstURL    string    `json:"first_url"`
	RequestedAt time.Time `json:"requested_at"`
	SHA256      string    `json:"sha256"`
}

// Request URLs are retained without the credential.
type page struct {
	Schema         string    `json:"schema"`
	IntentSHA256   string    `json:"intent_sha256"`
	PreviousSHA256 string    `json:"previous_sha256"`
	Index          int       `json:"index"`
	RequestURL     string    `json:"request_url"`
	ReceivedAt     time.Time `json:"received_at"`
	HTTPStatus     int       `json:"http_status"`
	BodySHA256     string    `json:"body_sha256"`
	BodyBytes      int       `json:"body_bytes"`
	SHA256         string    `json:"sha256"`
}
type completion struct {
	Schema         string    `json:"schema"`
	IntentSHA256   string    `json:"intent_sha256"`
	LastPageSHA256 string    `json:"last_page_sha256"`
	Pages          int       `json:"pages"`
	CompletedAt    time.Time `json:"completed_at"`
	SHA256         string    `json:"sha256"`
}
type failure struct {
	Schema       string    `json:"schema"`
	IntentSHA256 string    `json:"intent_sha256"`
	PagesHeld    int       `json:"pages_held"`
	FailedAt     time.Time `json:"failed_at"`
	HTTPStatus   int       `json:"http_status"`
	Reason       string    `json:"reason"`
	SHA256       string    `json:"sha256"`
}

func (r intent) seal() string     { r.SHA256 = ""; return workerplan.Hash(r) }
func (r page) seal() string       { r.SHA256 = ""; return workerplan.Hash(r) }
func (r completion) seal() string { r.SHA256 = ""; return workerplan.Hash(r) }
func (r failure) seal() string    { r.SHA256 = ""; return workerplan.Hash(r) }

type PageStatus struct {
	Index      int       `json:"index"`
	RequestURL string    `json:"request_url"`
	ReceivedAt time.Time `json:"received_at"`
	BodySHA256 string    `json:"body_sha256"`
	BodyBytes  int       `json:"body_bytes"`
	SHA256     string    `json:"sha256"`
}

// Status is derived from retained artifacts. The snapshot is rederived from the
// exact page bodies on every read, never stored as an independent claim.
type Status struct {
	Schema                   string       `json:"schema"`
	JournalID                string       `json:"journal_id"`
	CollectionID             string       `json:"collection_id"`
	State                    string       `json:"state"`
	Spec                     Spec         `json:"spec"`
	RequestedAt              time.Time    `json:"requested_at"`
	IntentSHA256             string       `json:"intent_sha256"`
	Pages                    []PageStatus `json:"pages"`
	CompletedAt              time.Time    `json:"completed_at,omitzero"`
	CompletionSHA256         string       `json:"completion_sha256,omitempty"`
	FailedAt                 time.Time    `json:"failed_at,omitzero"`
	HTTPStatus               int          `json:"http_status,omitempty"`
	FailureReason            string       `json:"failure_reason,omitempty"`
	ParseStatus              string       `json:"parse_status,omitempty"`
	ParseError               string       `json:"parse_error,omitempty"`
	Snapshot                 *Snapshot    `json:"snapshot,omitempty"`
	Authority                string       `json:"authority"`
	ProviderAttested         bool         `json:"provider_attested"`
	CoverageComplete         bool         `json:"coverage_complete"`
	EconomicApplicationReady bool         `json:"economic_application_ready"`
}

type Journal struct {
	*privatejournal.Journal
	origin   string
	now      func() time.Time
	interval time.Duration
	pause    func(context.Context, time.Duration) error
}

func OpenJournal(path string) (*Journal, error)         { return openJournal(path, true) }
func OpenExistingJournal(path string) (*Journal, error) { return openJournal(path, false) }

func openJournal(dir string, create bool) (*Journal, error) {
	p, err := privatejournal.Open(dir, create, journalSchema, "polygon-journal-")
	if err != nil {
		return nil, err
	}
	return &Journal{Journal: p, origin: Origin, now: time.Now, interval: pageInterval, pause: sleep}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func readJSON[T any](j *Journal, name string, value *T) (bool, error) {
	raw, err := j.Read(name, maxReceiptBytes)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if workerplan.ReadBoundedShape(raw, value, maxReceiptBytes) != nil {
		return false, errors.New("invalid polygon reference journal artifact " + name)
	}
	return true, nil
}

func pageName(id string, i int) string { return fmt.Sprintf("page-%s-%02d.json", id, i) }
func bodyName(id string, i int) string { return fmt.Sprintf("body-%s-%02d.json", id, i) }

// pages returns the verified page chain and exact bodies held for a collection.
func (j *Journal) pages(id string, in intent) ([]page, [][]byte, error) {
	var pages []page
	var bodies [][]byte
	previous := ""
	next := in.FirstURL
	for i := 1; i <= MaxPages; i++ {
		var p page
		found, err := readJSON(j, pageName(id, i), &p)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			break
		}
		body, err := j.Read(bodyName(id, i), MaxPageBytes)
		if err != nil || p.Schema != "mantik-polygon-reference-page-v1" || p.IntentSHA256 != in.SHA256 || p.PreviousSHA256 != previous || p.Index != i || p.RequestURL != next || p.HTTPStatus != http.StatusOK || p.ReceivedAt.Before(in.RequestedAt) || len(body) != p.BodyBytes || workerplan.Digest(body) != p.BodySHA256 || p.seal() != p.SHA256 {
			return nil, nil, ErrJournalConflict
		}
		env, err := decodePage(body)
		if err == nil && env.NextURL != "" {
			next, err = NextURL(in.FirstURL, env.NextURL)
		} else {
			next = ""
		}
		pages, bodies, previous = append(pages, p), append(bodies, body), p.SHA256
		if err != nil {
			break
		}
	}
	return pages, bodies, nil
}

// Collection rederives status from the retained intent, page chain, terminal
// receipt and exact bodies, rejecting artifacts that break the hash chain.
func (j *Journal) Collection(id string) (Status, error) {
	if !collectionID.MatchString(id) {
		return Status{}, errors.New("invalid polygon reference collection identity")
	}
	var in intent
	found, err := readJSON(j, "intent-"+id+".json", &in)
	if err != nil {
		return Status{}, err
	}
	if !found {
		return Status{}, ErrCollectionNotFound
	}
	if in.Schema != "mantik-polygon-reference-intent-v1" || in.JournalID != j.ID || in.Spec.ID != id || in.Spec.Validate() != nil || in.FirstURL != in.Spec.FirstURL() || in.RequestedAt.IsZero() || in.seal() != in.SHA256 {
		return Status{}, ErrJournalConflict
	}
	pages, bodies, err := j.pages(id, in)
	if err != nil {
		return Status{}, err
	}
	s := Status{Schema: "mantik-polygon-reference-collection-v1", JournalID: j.ID, CollectionID: id, State: "interrupted", Spec: in.Spec, RequestedAt: in.RequestedAt, IntentSHA256: in.SHA256, Pages: []PageStatus{}, Authority: "local_journal_acquisition_of_polygon_reference_data_only"}
	for _, p := range pages {
		s.Pages = append(s.Pages, PageStatus{Index: p.Index, RequestURL: p.RequestURL, ReceivedAt: p.ReceivedAt, BodySHA256: p.BodySHA256, BodyBytes: p.BodyBytes, SHA256: p.SHA256})
	}
	var done completion
	completed, err := readJSON(j, "complete-"+id+".json", &done)
	if err != nil {
		return Status{}, err
	}
	var failed failure
	isFailed, err := readJSON(j, "failed-"+id+".json", &failed)
	if err != nil {
		return Status{}, err
	}
	if completed && isFailed {
		return Status{}, ErrJournalConflict
	}
	if isFailed {
		if failed.Schema != "mantik-polygon-reference-failure-v1" || failed.IntentSHA256 != in.SHA256 || failed.PagesHeld != len(pages) || failed.FailedAt.Before(in.RequestedAt) || failed.Reason == "" || failed.seal() != failed.SHA256 {
			return Status{}, ErrJournalConflict
		}
		s.State, s.FailedAt, s.HTTPStatus, s.FailureReason = "failed", failed.FailedAt, failed.HTTPStatus, failed.Reason
		return s, nil
	}
	if !completed {
		return s, nil
	}
	if done.Schema != "mantik-polygon-reference-completion-v1" || done.IntentSHA256 != in.SHA256 || done.Pages != len(pages) || len(pages) == 0 || done.LastPageSHA256 != pages[len(pages)-1].SHA256 || done.CompletedAt.Before(pages[len(pages)-1].ReceivedAt) || done.seal() != done.SHA256 {
		return Status{}, ErrJournalConflict
	}
	s.State, s.CompletedAt, s.CompletionSHA256 = "retained", done.CompletedAt, done.SHA256
	snapshot, err := Parse(in.Spec, bodies)
	if err != nil {
		s.ParseStatus, s.ParseError = "unparseable", err.Error()
		return s, nil
	}
	s.ParseStatus, s.Snapshot = "parsed", &snapshot
	return s, nil
}

// Collect follows one complete query: the first page and each cursor
// continuation, paced and bounded. A completed identity returns its retained
// status without fetching. A failed or interrupted identity is never resumed.
func (j *Journal) Collect(ctx context.Context, s Spec, apiKey string) (Status, error) {
	if err := s.Validate(); err != nil {
		return Status{}, err
	}
	old, err := j.Collection(s.ID)
	if err == nil {
		if old.Spec != s {
			return Status{}, ErrJournalConflict
		}
		if old.State != "retained" {
			return old, ErrCollectionUnresolved
		}
		return old, nil
	}
	if !errors.Is(err, ErrCollectionNotFound) {
		return Status{}, err
	}
	if apiKey == "" || len(apiKey) > 256 || strings.ContainsFunc(apiKey, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return Status{}, errors.New("polygon collection requires a valid API key")
	}
	in := intent{Schema: "mantik-polygon-reference-intent-v1", JournalID: j.ID, Spec: s, FirstURL: s.FirstURL(), RequestedAt: j.now().UTC()}
	in.SHA256 = in.seal()
	raw, _ := json.Marshal(in)
	if err := j.WriteOnce("intent-"+s.ID+".json", raw, maxReceiptBytes); err != nil {
		return Status{}, err
	}
	held, previous := 0, ""
	fail := func(status int, reason string) (Status, error) {
		f := failure{Schema: "mantik-polygon-reference-failure-v1", IntentSHA256: in.SHA256, PagesHeld: held, FailedAt: j.now().UTC(), HTTPStatus: status, Reason: reason}
		if f.FailedAt.Before(in.RequestedAt) {
			f.FailedAt = in.RequestedAt
		}
		f.SHA256 = f.seal()
		raw, _ := json.Marshal(f)
		if err := j.WriteOnce("failed-"+s.ID+".json", raw, maxReceiptBytes); err != nil {
			return Status{}, err
		}
		st, err := j.Collection(s.ID)
		return st, errors.Join(errors.New("polygon reference acquisition failed: "+reason), err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	next := in.FirstURL
	var last page
	for next != "" {
		if held == MaxPages {
			return fail(0, "page_bound_reached")
		}
		if held > 0 {
			if err := j.pause(ctx, j.interval); err != nil {
				return fail(0, "cancelled_between_pages")
			}
		}
		target := strings.Replace(next, Origin, j.origin, 1)
		separator := "&"
		if !strings.Contains(target, "?") {
			separator = "?"
		}
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target+separator+url.Values{"apiKey": {apiKey}}.Encode(), nil)
		if err != nil {
			cancel()
			return fail(0, "request_construction_failed")
		}
		response, err := client.Do(req)
		if err != nil {
			cancel()
			return fail(0, "transport_error")
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, MaxPageBytes+1))
		_ = response.Body.Close()
		cancel()
		if response.StatusCode != http.StatusOK {
			return fail(response.StatusCode, fmt.Sprintf("http_status_%d", response.StatusCode))
		}
		if readErr != nil || len(body) == 0 || len(body) > MaxPageBytes {
			return fail(response.StatusCode, "page_size_outside_bounds")
		}
		env, err := decodePage(body)
		if err != nil {
			return fail(response.StatusCode, "invalid_page_envelope")
		}
		continuation := ""
		if env.NextURL != "" {
			if continuation, err = NextURL(in.FirstURL, env.NextURL); err != nil {
				return fail(response.StatusCode, "invalid_continuation")
			}
		}
		held++
		if err := j.WriteOnce(bodyName(s.ID, held), body, MaxPageBytes); err != nil {
			return Status{}, err
		}
		last = page{Schema: "mantik-polygon-reference-page-v1", IntentSHA256: in.SHA256, PreviousSHA256: previous, Index: held, RequestURL: next, ReceivedAt: j.now().UTC(), HTTPStatus: response.StatusCode, BodySHA256: workerplan.Digest(body), BodyBytes: len(body)}
		if last.ReceivedAt.Before(in.RequestedAt) {
			last.ReceivedAt = in.RequestedAt
		}
		last.SHA256 = last.seal()
		raw, _ := json.Marshal(last)
		if err := j.WriteOnce(pageName(s.ID, held), raw, maxReceiptBytes); err != nil {
			return Status{}, err
		}
		previous, next = last.SHA256, continuation
	}
	done := completion{Schema: "mantik-polygon-reference-completion-v1", IntentSHA256: in.SHA256, LastPageSHA256: last.SHA256, Pages: held, CompletedAt: j.now().UTC()}
	if done.CompletedAt.Before(last.ReceivedAt) {
		done.CompletedAt = last.ReceivedAt
	}
	done.SHA256 = done.seal()
	raw, _ = json.Marshal(done)
	if err := j.WriteOnce("complete-"+s.ID+".json", raw, maxReceiptBytes); err != nil {
		return Status{}, err
	}
	return j.Collection(s.ID)
}
