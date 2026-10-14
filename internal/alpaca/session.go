package alpaca

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"
)

var ErrSessionIncomplete = errors.New("session has no terminal receipt; inspect actual runtime and retained collections before starting a new session identity")
var ErrSessionStopped = errors.New("session stopped before completing its declared collections")

// SessionSpec is a finite retention budget. Collection.ID is a template field
// and must be empty: each acquisition has its own derived immutable identity.
type SessionSpec struct {
	ID              string         `json:"id"`
	Count           int            `json:"count"`
	IntervalSeconds int            `json:"interval_seconds"`
	Collection      CollectionSpec `json:"collection"`
}
type sessionIntent struct {
	Schema    string      `json:"schema"`
	JournalID string      `json:"journal_id"`
	Spec      SessionSpec `json:"spec"`
	At        time.Time   `json:"at"`
}
type sessionCompletion struct {
	Schema       string    `json:"schema"`
	IntentSHA256 string    `json:"intent_sha256"`
	State        string    `json:"state"`
	Code         string    `json:"code"`
	At           time.Time `json:"at"`
	Started      int       `json:"started"`
	Retained     int       `json:"retained"`
}
type SessionStatus struct {
	ID          string             `json:"id"`
	State       string             `json:"state"`
	Code        string             `json:"code"`
	Declared    int                `json:"declared"`
	Started     int                `json:"started"`
	Retained    int                `json:"retained"`
	Collections []CollectionStatus `json:"collections"`
}

func sessionName(id, suffix string) string { return "session-" + id + "-" + suffix + ".json" }
func (s SessionSpec) collection(n int) CollectionSpec {
	c := s.Collection
	c.ID = fmt.Sprintf("%s-%03d", s.ID, n)
	return c
}
func (s SessionSpec) validate() error {
	if !capturePattern.MatchString(s.ID) || len(s.ID) > 32 || s.Count < 1 || s.Count > 16 || s.IntervalSeconds < 1 || s.IntervalSeconds > 300 || s.Collection.ID != "" {
		return errors.New("session requires identity (1–32 characters), 1–16 collections, 1–300 second interval, and an empty collection template identity")
	}
	return s.collection(1).validate()
}

// Session status verifies retained history; it never treats an incomplete
// receipt as a running process and never resumes a historical schedule.
func (j *Journal) Session(id string) (SessionStatus, error) {
	out := SessionStatus{ID: id, State: "incomplete", Code: "terminal_receipt_missing", Collections: []CollectionStatus{}}
	if !capturePattern.MatchString(id) || len(id) > 32 {
		return out, errors.New("invalid session identity")
	}
	var intent sessionIntent
	digest, err := j.readCollection(sessionName(id, "intent"), &intent)
	if err != nil {
		return out, err
	}
	if intent.Schema != "mantik-alpaca-session-v1" || intent.JournalID != j.ID || intent.Spec.ID != id || !validTime(intent.At) || intent.Spec.validate() != nil {
		return out, ErrJournalConflict
	}
	out.Declared = intent.Spec.Count
	gap := false
	stopped := false
	for n := 1; n <= intent.Spec.Count; n++ {
		spec := intent.Spec.collection(n)
		status, err := j.Collection(spec.ID)
		if errors.Is(err, os.ErrNotExist) {
			// Missing intent cannot erase orphan attempt/terminal/capture history.
			for _, suffix := range []string{"start-1", "start-2", "start-3", "response-1", "response-2", "response-3", "complete"} {
				if _, e := j.read(collectionName(spec.ID, suffix), maxJournalFile); !errors.Is(e, os.ErrNotExist) {
					return out, ErrJournalConflict
				}
			}
			if _, e := j.read(captureName(spec.recording(Response{}, j.ID)), maxJournalFile); !errors.Is(e, os.ErrNotExist) {
				return out, ErrJournalConflict
			}
			gap = true
			continue
		}
		if err != nil {
			return out, err
		}
		if gap || stopped {
			return out, ErrJournalConflict
		}
		var collection collectionIntent
		if _, err := j.readCollection(collectionName(spec.ID, "intent"), &collection); err != nil {
			return out, err
		}
		if !reflect.DeepEqual(collection.Spec, spec) || collection.At.Before(intent.At) {
			return out, ErrJournalConflict
		}
		out.Started++
		out.Collections = append(out.Collections, status)
		if status.State == "retained" {
			out.Retained++
		} else {
			stopped = true
		}
	}
	var done sessionCompletion
	_, err = j.readCollection(sessionName(id, "complete"), &done)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if done.Schema != "mantik-alpaca-session-result-v1" || done.IntentSHA256 != digest || !validTime(done.At) || done.At.Before(intent.At) || done.Started != out.Started || done.Retained != out.Retained {
		return out, ErrJournalConflict
	}
	switch done.State {
	case "completed":
		if done.Code != "declared_count_retained" || out.Retained != out.Declared {
			return out, ErrJournalConflict
		}
	case "stopped":
		if done.Code != "collection_failed" && done.Code != "cancelled" {
			return out, ErrJournalConflict
		}
		if done.Code == "collection_failed" && (out.Started == 0 || out.Collections[len(out.Collections)-1].State != "failed") {
			return out, ErrJournalConflict
		}
		if out.Retained == out.Declared {
			return out, ErrJournalConflict
		}
	default:
		return out, ErrJournalConflict
	}
	out.State, out.Code = done.State, done.Code
	return out, nil
}

// RunSession owns the journal across acquisition and pacing. No persistent
// scheduler, rotation, automatic restart, catch-up, or service mutation is added.
func (j *Journal) RunSession(ctx context.Context, c *Client, s SessionSpec) (SessionStatus, error) {
	return j.runSession(ctx, c, s, wait)
}
func (j *Journal) runSession(ctx context.Context, c *Client, s SessionSpec, pause func(context.Context, time.Duration) error) (SessionStatus, error) {
	out := SessionStatus{ID: s.ID}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := s.validate(); err != nil {
		return out, err
	}
	if c == nil || c.feed != s.Collection.Feed {
		return out, errors.New("client feed must match session contract")
	}
	if old, err := j.Session(s.ID); err == nil {
		var intent sessionIntent
		if _, err := j.readCollection(sessionName(s.ID, "intent"), &intent); err != nil {
			return out, err
		}
		if !reflect.DeepEqual(intent.Spec, s) {
			return out, ErrJournalConflict
		}
		if old.State == "completed" {
			return old, nil
		}
		if old.State == "stopped" {
			return old, ErrSessionStopped
		}
		return old, ErrSessionIncomplete
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, err
	}
	if _, err := j.read(sessionName(s.ID, "complete"), maxJournalFile); !errors.Is(err, os.ErrNotExist) {
		return out, ErrJournalConflict
	}
	captures, err := j.captures()
	if err != nil {
		return out, err
	}
	directory, err := j.root.Open(".")
	if err != nil {
		return out, err
	}
	entries, readErr := directory.ReadDir(20001)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return out, readErr
	}
	if closeErr != nil {
		return out, closeErr
	}
	collections, sessions := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "collection-") && strings.HasSuffix(name, "-intent.json") {
			collections++
		}
		if strings.HasPrefix(name, "session-") && strings.HasSuffix(name, "-intent.json") {
			sessions++
		}
		for n := 1; n <= s.Count; n++ {
			id := s.collection(n).ID
			if name == captureName(s.collection(n).recording(Response{}, j.ID)) {
				return out, ErrJournalConflict
			}
			for _, suffix := range []string{"intent", "complete", "start-1", "start-2", "start-3", "response-1", "response-2", "response-3"} {
				if name == collectionName(id, suffix) {
					return out, ErrJournalConflict
				}
			}
		}
	}
	// Reserve all declared acquisitions and their publication headroom up front.
	// Per-collection checks remain in force; journal ownership prevents new writers.
	if len(captures)+s.Count > MaxCaptures || collections+s.Count > MaxCollections || sessions >= 128 || len(entries)+s.Count*20+4 > 20000 {
		return out, errors.New("session retention budget exceeds journal capacity before acquisition")
	}
	intent := sessionIntent{"mantik-alpaca-session-v1", j.ID, s, time.Now().UTC()}
	if err := j.writeCollection(sessionName(s.ID, "intent"), intent); err != nil {
		return out, err
	}
	digest, err := j.readCollection(sessionName(s.ID, "intent"), &intent)
	if err != nil {
		return out, err
	}
	finish := func(state, code string) (SessionStatus, error) {
		current, err := j.Session(s.ID)
		if err != nil {
			return out, err
		}
		if err := j.writeCollection(sessionName(s.ID, "complete"), sessionCompletion{"mantik-alpaca-session-result-v1", digest, state, code, time.Now().UTC(), current.Started, current.Retained}); err != nil {
			return current, err
		}
		return j.Session(s.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	for n := 1; n <= s.Count; n++ {
		if n > 1 {
			if err := pause(ctx, time.Duration(s.IntervalSeconds)*time.Second); err != nil {
				status, receiptErr := finish("stopped", "cancelled")
				return status, errors.Join(ErrSessionStopped, err, receiptErr)
			}
		}
		if err := ctx.Err(); err != nil {
			status, receiptErr := finish("stopped", "cancelled")
			return status, errors.Join(ErrSessionStopped, err, receiptErr)
		}
		readCtx, readCancel := context.WithTimeout(ctx, 2*time.Minute)
		status, err := j.Collect(readCtx, c, s.collection(n))
		readCancel()
		if err != nil {
			if status.State == "failed" {
				result, receiptErr := finish("stopped", "collection_failed")
				return result, errors.Join(ErrSessionStopped, receiptErr)
			}
			// Missing completion or persistence failure stays incomplete. The original
			// per-collection evidence, not an invented session success, governs recovery.
			current, statusErr := j.Session(s.ID)
			return current, errors.Join(ErrSessionIncomplete, err, statusErr)
		}
	}
	return finish("completed", "declared_count_retained")
}
