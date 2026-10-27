package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"
)

const MaxCollections = 128

var ErrCollectionIncomplete = errors.New("collection has no terminal receipt; inspect retained attempts, do not repeat this collection identity")
var ErrCollectionFailed = errors.New("collection failed; inspect retained collection status and attempt artifacts")

// CollectionSpec is explicit attribution, not evidence of entitlement. A new
// retrieval requires a new identity; the successful recording uses revision 1.
type CollectionSpec struct {
	ID                 string   `json:"id"`
	Capability         string   `json:"capability"`
	Feed               string   `json:"feed"`
	Symbols            []string `json:"symbols"`
	Start              string   `json:"start,omitempty"`
	End                string   `json:"end,omitempty"`
	AccessRef          string   `json:"access_ref"`
	MaxQuoteAgeSeconds int64    `json:"max_quote_age_seconds"`
}

// Raw is JSON base64 so even invalid UTF-8 survives byte-for-byte inspection.
// Partial records only a bounded prefix, never a complete-response claim.
type ReadAttempt struct {
	Number     int       `json:"number"`
	State      string    `json:"state"`
	At         time.Time `json:"at"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Raw        []byte    `json:"raw_base64,omitempty"`
	Partial    bool      `json:"partial,omitempty"`
}
type collectionIntent struct {
	Schema    string         `json:"schema"`
	JournalID string         `json:"journal_id"`
	Spec      CollectionSpec `json:"spec"`
	At        time.Time      `json:"at"`
}
type collectionCompletion struct {
	Schema       string    `json:"schema"`
	IntentSHA256 string    `json:"intent_sha256"`
	State        string    `json:"state"`
	Code         string    `json:"code"`
	At           time.Time `json:"at"`
	Capture      string    `json:"capture,omitempty"`
}
type CollectionStatus struct {
	ID                string `json:"id"`
	State             string `json:"state"`
	Code              string `json:"code"`
	Attempts          int    `json:"attempts"`
	Responses         int    `json:"responses"`
	LastHTTPStatus    int    `json:"last_http_status,omitempty"`
	UnfinishedAttempt bool   `json:"unfinished_attempt"`
	Capture           string `json:"capture,omitempty"`
}
type collectionArtifact struct {
	Payload json.RawMessage `json:"payload"`
	SHA256  string          `json:"sha256"`
}

func (j *Journal) writeCollection(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	envelope, err := json.Marshal(collectionArtifact{raw, Digest(string(raw))})
	if err != nil {
		return err
	}
	return j.writeOnce(name, envelope)
}
func (j *Journal) readCollection(name string, value any) (string, error) {
	raw, err := j.read(name, maxJournalFile)
	if err != nil {
		return "", err
	}
	var envelope collectionArtifact
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.SHA256 != Digest(string(envelope.Payload)) {
		return "", ErrJournalConflict
	}
	if err := json.Unmarshal(envelope.Payload, value); err != nil {
		return "", ErrJournalConflict
	}
	return envelope.SHA256, nil
}
func collectionName(id, suffix string) string { return "collection-" + id + "-" + suffix + ".json" }
func (s CollectionSpec) recording(response Response, journalID string) Recording {
	feed := s.Feed
	if s.Capability == "corporate_actions" {
		feed = CorporateActionFeed
	} else if s.Capability == "market_calendar" {
		feed = "regular_session"
	}
	return Recording{Schema: RecordingSchema, Currency: "USD", CaptureID: s.ID, Capability: s.Capability, Feed: feed, Symbols: s.Symbols, Start: s.Start, End: s.End, Revision: 1, ReceivedAt: response.ReceivedAt, EvidenceClass: "prospective_market_unqualified", AccessRef: s.AccessRef, RawRef: "journal:" + journalID + ":" + collectionName(s.ID, fmt.Sprintf("response-%d", response.Attempts)), Original: response.Original, SHA256: response.SHA256, MaxQuoteAgeSeconds: s.MaxQuoteAgeSeconds}
}
func (s CollectionSpec) validate() error {
	if !capturePattern.MatchString(s.ID) || (s.Feed != "iex" && s.Feed != "sip" && s.Feed != "delayed_sip") {
		return errors.New("collection requires a bounded identity and explicit supported feed")
	}
	raw := `{"quotes":{}}`
	if s.Capability == "corporate_actions" {
		raw = `{"corporate_actions":{},"next_page_token":null}`
	} else if s.Capability == "market_calendar" {
		raw = `[]`
	}
	r := s.recording(Response{Original: raw, SHA256: Digest(raw), ReceivedAt: time.Now().UTC(), Attempts: 1}, "validation")
	_, err := Normalize(r)
	return err
}

// Collection reads actual immutable evidence. An intent without completion
// remains incomplete even when a response or capture exists; status is not a
// live process handle and does not resume acquisition.
func (j *Journal) Collection(id string) (CollectionStatus, error) {
	out := CollectionStatus{ID: id, State: "incomplete", Code: "terminal_receipt_missing"}
	if !capturePattern.MatchString(id) {
		return out, errors.New("invalid collection identity")
	}
	var intent collectionIntent
	digest, err := j.readCollection(collectionName(id, "intent"), &intent)
	if err != nil {
		return out, err
	}
	if intent.Schema != "mantik-alpaca-collection-v1" || intent.JournalID != j.ID || intent.Spec.ID != id || !validTime(intent.At) || intent.Spec.validate() != nil {
		return out, ErrJournalConflict
	}
	ended := false
	for n := 1; n <= 3; n++ {
		var start ReadAttempt
		_, err := j.readCollection(collectionName(id, fmt.Sprintf("start-%d", n)), &start)
		if errors.Is(err, os.ErrNotExist) {
			if _, responseErr := j.read(collectionName(id, fmt.Sprintf("response-%d", n)), maxJournalFile); !errors.Is(responseErr, os.ErrNotExist) {
				return out, ErrJournalConflict
			}
			ended = true
			continue
		}
		if err != nil {
			return out, err
		}
		if ended || start.Number != n || start.State != "started" || !validTime(start.At) || start.HTTPStatus != 0 || len(start.Raw) != 0 || start.Partial || out.UnfinishedAttempt {
			return out, ErrJournalConflict
		}
		out.Attempts++
		var response ReadAttempt
		_, err = j.readCollection(collectionName(id, fmt.Sprintf("response-%d", n)), &response)
		if errors.Is(err, os.ErrNotExist) {
			out.UnfinishedAttempt = true
			continue
		}
		if err != nil {
			return out, err
		}
		if response.Number != n || !validTime(response.At) || response.At.Before(start.At) || len(response.Raw) > MaxBytes || (response.State != "response" && response.State != "transport_failed") || (response.State == "response" && (response.HTTPStatus < 100 || response.HTTPStatus > 599)) || (response.State == "transport_failed" && (response.HTTPStatus != 0 || len(response.Raw) != 0 || response.Partial)) {
			return out, ErrJournalConflict
		}
		out.Responses++
		out.LastHTTPStatus = response.HTTPStatus
	}
	var done collectionCompletion
	_, err = j.readCollection(collectionName(id, "complete"), &done)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if done.Schema != "mantik-alpaca-collection-result-v1" || done.IntentSHA256 != digest || !validTime(done.At) || done.At.Before(intent.At) || (done.State != "retained" && done.State != "failed") || done.Code == "" {
		return out, ErrJournalConflict
	}
	if done.State == "retained" {
		if done.Code != "capture_retained" || out.Attempts < 1 || out.UnfinishedAttempt || done.Capture != captureName(intent.Spec.recording(Response{}, j.ID)) {
			return out, ErrJournalConflict
		}
		var last ReadAttempt
		if _, err := j.readCollection(collectionName(id, fmt.Sprintf("response-%d", out.Attempts)), &last); err != nil {
			return out, err
		}
		if last.State != "response" || last.HTTPStatus != 200 || last.Partial {
			return out, ErrJournalConflict
		}
		recording := intent.Spec.recording(Response{Original: string(last.Raw), SHA256: Digest(string(last.Raw)), ReceivedAt: last.At, Attempts: last.Number}, j.ID)
		all, err := j.captures()
		if err != nil {
			return out, err
		}
		found := false
		for _, c := range all {
			if captureName(c.Recording) == done.Capture {
				originalRef := recording.RawRef
				recording.RawRef = "journal:" + j.ID + ":" + done.Capture
				found = reflect.DeepEqual(recording, c.Recording) && c.OriginalRef == originalRef
			}
		}
		if !found {
			return out, ErrJournalConflict
		}
	} else if done.Capture != "" || (done.Code != "read_failed" && done.Code != "normalization_failed") {
		return out, ErrJournalConflict
	}
	out.State, out.Code, out.Capture = done.State, done.Code, done.Capture
	return out, nil
}

// Collect performs one bounded read, never submission or scheduling. Persisted
// identity reuse returns a verified receipt or stops at an incomplete/failed
// run; it never re-requests an old latest quote under a historical identity.
func (j *Journal) Collect(ctx context.Context, c *Client, s CollectionSpec) (CollectionStatus, error) {
	out := CollectionStatus{ID: s.ID}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.validate(); err != nil {
		return out, err
	}
	if c == nil || c.feed != s.Feed {
		return out, errors.New("client feed must match collection contract")
	}
	if old, err := j.Collection(s.ID); err == nil {
		var intent collectionIntent
		if _, err := j.readCollection(collectionName(s.ID, "intent"), &intent); err != nil {
			return out, err
		}
		if !reflect.DeepEqual(intent.Spec, s) {
			return out, ErrJournalConflict
		}
		if old.State == "retained" {
			return old, nil
		}
		if old.State == "failed" {
			return old, ErrCollectionFailed
		}
		return old, ErrCollectionIncomplete
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	for _, suffix := range []string{"complete", "start-1", "start-2", "start-3", "response-1", "response-2", "response-3"} {
		if _, err := j.read(collectionName(s.ID, suffix), maxJournalFile); err == nil {
			return out, ErrJournalConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			return out, err
		}
	}
	captures, err := j.captures()
	if err != nil {
		return out, err
	}
	if len(captures) >= MaxCaptures {
		return out, errors.New("journal capture capacity reached before acquisition")
	}
	// Bound failed and interrupted acquisitions separately from valid captures.
	dir, err := j.root.Open(".")
	if err != nil {
		return out, err
	}
	entries, readErr := dir.ReadDir(20001)
	closeErr := dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return out, readErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	count := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "collection-") && strings.HasSuffix(entry.Name(), "-intent.json") {
			count++
		}
	}
	if count >= MaxCollections || len(entries) > 19980 {
		return out, errors.New("journal collection capacity reached before acquisition")
	}
	// Existing manual capture identities cannot be adopted by a fresh fetch.
	for _, capture := range captures {
		if capture.Recording.CaptureID == s.ID {
			return out, ErrJournalConflict
		}
	}
	intent := collectionIntent{"mantik-alpaca-collection-v1", j.ID, s, time.Now().UTC()}
	if err := j.writeCollection(collectionName(s.ID, "intent"), intent); err != nil {
		return out, err
	}
	intentRaw, _ := json.Marshal(intent)
	finish := func(state, code, capture string) (CollectionStatus, error) {
		done := collectionCompletion{"mantik-alpaca-collection-result-v1", Digest(string(intentRaw)), state, code, time.Now().UTC(), capture}
		if err := j.writeCollection(collectionName(s.ID, "complete"), done); err != nil {
			return out, err
		}
		return j.Collection(s.ID)
	}
	query := Query{Capability: s.Capability, Start: s.Start, End: s.End}
	if s.Capability == "latest_quotes" || s.Capability == "corporate_actions" {
		query.Symbols = s.Symbols
	}
	persistenceFailed := false
	response, fetchErr := c.fetch(ctx, query, func(attempt ReadAttempt) error {
		suffix := fmt.Sprintf("response-%d", attempt.Number)
		if attempt.State == "started" {
			suffix = fmt.Sprintf("start-%d", attempt.Number)
		}
		err := j.writeCollection(collectionName(s.ID, suffix), attempt)
		if err != nil {
			persistenceFailed = true
		}
		return err
	})
	if persistenceFailed {
		return out, ErrCollectionIncomplete
	}
	if fetchErr != nil {
		status, err := finish("failed", "read_failed", "")
		return status, errors.Join(ErrCollectionFailed, err)
	}
	recording := s.recording(response, j.ID)
	if _, err := Normalize(recording); err != nil {
		status, err := finish("failed", "normalization_failed", "")
		return status, errors.Join(ErrCollectionFailed, err)
	}
	if _, err := j.Retain(recording); err != nil {
		return out, ErrCollectionIncomplete
	}
	return finish("retained", "capture_retained", captureName(recording))
}
