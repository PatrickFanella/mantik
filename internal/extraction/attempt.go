package extraction

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/news"
)

const AttemptSchema = "mantik-news-extraction-attempt-v1"
const HistorySchema = "mantik-news-extraction-history-v1"
const MaxAttempts = 32

var ID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var receiverID = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var sha = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrInvalid = errors.New("invalid extraction attempt or retained evidence")
var ErrConflict = errors.New("extraction identity, clock or completion conflict")
var ErrCapacity = errors.New("extraction attempt capacity reached")
var ErrNotFound = errors.New("extraction attempt not found")

type Attempt struct {
	Schema         string       `json:"schema"`
	ReceiverID     string       `json:"receiver_id"`
	ID             string       `json:"id"`
	Mode           string       `json:"mode"`
	Contract       Contract     `json:"contract"`
	ContractSHA256 string       `json:"contract_sha256"`
	Article        news.Article `json:"article"`
	ArticleSHA256  string       `json:"article_sha256"`
	EventKey       string       `json:"event_key"`
	StartedAt      time.Time    `json:"started_at"`
	Deadline       time.Time    `json:"deadline"`
	Status         string       `json:"status"`
	CompletedAt    time.Time    `json:"completed_at"`
	Result         *Result      `json:"result,omitempty"`
	SHA256         string       `json:"sha256"`
}

func validAt(t time.Time) bool { return !t.IsZero() && t.Year() > 0 && t.Year() <= 9999 }
func seal(a Attempt) Attempt   { a.SHA256 = ""; a.SHA256 = hash(a); return a }

func Start(receiver, id string, article news.Article, key string, at time.Time) (Attempt, error) {
	a, err := article.Canonical()
	at = at.UTC()
	contract := Definition()
	deadline := at.Add(time.Duration(contract.TimeoutSeconds) * time.Second)
	if err != nil || a.Schema != news.ArticleSchema || !receiverID.MatchString(receiver) || !ID.MatchString(id) || !eventKey.MatchString(key) || !validAt(at) || !validAt(deadline) {
		return Attempt{}, ErrInvalid
	}
	if at.Before(a.ReceivedAt) {
		return Attempt{}, ErrConflict
	}
	return seal(Attempt{Schema: AttemptSchema, ReceiverID: receiver, ID: id, Mode: "recorded_model_fixture",
		Contract: contract, ContractSHA256: hash(contract), Article: a, ArticleSHA256: hash(a),
		EventKey: key, StartedAt: at, Deadline: deadline, Status: "pending"}), nil
}

func SameStart(a Attempt, receiver string, article news.Article, key string) bool {
	canonical, err := article.Canonical()
	return err == nil && a.ReceiverID == receiver && a.EventKey == key && reflect.DeepEqual(a.Article, canonical)
}

type recordedArtifact []byte

func (r recordedArtifact) Extract(ctx context.Context, _ Frame) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(r), nil
}

// EvaluateRecorded has no provider callback. Its exact bounded bytes may be
// malformed; those are interpreted into a retained abstention, not discarded.
func EvaluateRecorded(ctx context.Context, a Attempt, raw []byte) (Result, error) {
	if len(raw) > MaxArtifactBytes {
		return Result{}, ErrInvalid
	}
	r, err := Run(ctx, a.Article, a.EventKey, recordedArtifact(raw))
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	return r, nil
}

func (a Attempt) Validate() error {
	pending, err := Start(a.ReceiverID, a.ID, a.Article, a.EventKey, a.StartedAt)
	if err != nil || a.Schema != AttemptSchema || a.Mode != pending.Mode || !reflect.DeepEqual(a.Contract, pending.Contract) || a.ContractSHA256 != pending.ContractSHA256 || a.ArticleSHA256 != pending.ArticleSHA256 || !reflect.DeepEqual(a.Article, pending.Article) || !a.Deadline.Equal(pending.Deadline) || a.SHA256 != seal(a).SHA256 {
		return ErrInvalid
	}
	if a.Status == "pending" {
		if a.Result != nil || !a.CompletedAt.IsZero() {
			return ErrInvalid
		}
		return nil
	}
	if !slices.Contains([]string{"completed", "expired"}, a.Status) || !validAt(a.CompletedAt) || a.CompletedAt.Before(a.StartedAt) || (a.Status == "expired") != !a.CompletedAt.Before(a.Deadline) || a.Result == nil || !a.Result.Artifact.CaptureComplete || len(a.Result.Artifact.RawBase64) > base64.StdEncoding.EncodedLen(MaxArtifactBytes) {
		return ErrInvalid
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(a.Result.Artifact.RawBase64)
	if err != nil {
		return ErrInvalid
	}
	expected, err := EvaluateRecorded(context.Background(), pending, raw)
	if err != nil || !reflect.DeepEqual(*a.Result, expected) {
		return ErrInvalid
	}
	return nil
}

// Commit rechecks immutable source/result pins after storage reacquires its lock.
// Exact completed retries recover the original receipt without reading a clock.
func Commit(a Attempt, raw []byte, result Result, now func() time.Time) (Attempt, error) {
	if a.Validate() != nil || len(raw) > MaxArtifactBytes {
		return Attempt{}, ErrInvalid
	}
	if a.Status != "pending" {
		if a.Result.Artifact.RawBase64 != base64.StdEncoding.EncodeToString(raw) {
			return Attempt{}, ErrConflict
		}
		return a, nil
	}
	expected, err := EvaluateRecorded(context.Background(), a, raw)
	if err != nil || !reflect.DeepEqual(expected, result) {
		return Attempt{}, ErrInvalid
	}
	at := now().UTC()
	if !validAt(at) || at.Before(a.StartedAt) {
		return Attempt{}, ErrConflict
	}
	a.CompletedAt, a.Status, a.Result = at, "completed", &expected
	if !at.Before(a.Deadline) {
		a.Status = "expired"
	}
	return seal(a), nil
}

type Summary struct {
	ID            string    `json:"id"`
	AttemptSHA256 string    `json:"attempt_sha256"`
	ArticleSHA256 string    `json:"article_sha256"`
	SourceID      string    `json:"source_id"`
	ArticleID     string    `json:"article_id"`
	Revision      int       `json:"revision"`
	EventKey      string    `json:"event_key"`
	StartedAt     time.Time `json:"started_at"`
	Deadline      time.Time `json:"deadline"`
	CompletedAt   time.Time `json:"completed_at"`
	Status        string    `json:"status"`
	Outcome       string    `json:"outcome,omitempty"`
	ReasonCode    string    `json:"reason_code,omitempty"`
	ResultSHA256  string    `json:"result_sha256,omitempty"`
}
type History struct {
	Schema              string         `json:"schema"`
	ReceiverID          string         `json:"receiver_id"`
	Attempts            []Summary      `json:"attempts"`
	AttemptCount        int            `json:"attempt_count"`
	StatusCounts        map[string]int `json:"status_counts"`
	Authority           string         `json:"authority"`
	QualificationCredit bool           `json:"qualification_credit"`
	SHA256              string         `json:"sha256"`
}

func MakeHistory(receiver string, all []Attempt) (History, error) {
	if !receiverID.MatchString(receiver) || len(all) > MaxAttempts {
		return History{}, ErrInvalid
	}
	h := History{Schema: HistorySchema, ReceiverID: receiver, Attempts: []Summary{}, StatusCounts: map[string]int{"pending": 0, "completed": 0, "expired": 0}, Authority: "recorded_extraction_history_only"}
	for _, a := range all {
		if a.ReceiverID != receiver || a.Validate() != nil {
			return History{}, ErrInvalid
		}
		s := Summary{ID: a.ID, AttemptSHA256: a.SHA256, ArticleSHA256: a.ArticleSHA256, SourceID: a.Article.SourceID, ArticleID: a.Article.ArticleID, Revision: a.Article.Revision, EventKey: a.EventKey, StartedAt: a.StartedAt, Deadline: a.Deadline, CompletedAt: a.CompletedAt, Status: a.Status}
		if a.Result != nil {
			s.Outcome, s.ReasonCode, s.ResultSHA256 = a.Result.Outcome, a.Result.ReasonCode, a.Result.SHA256
		}
		h.Attempts = append(h.Attempts, s)
		h.StatusCounts[a.Status]++
	}
	slices.SortFunc(h.Attempts, func(a, b Summary) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	h.AttemptCount = len(h.Attempts)
	h.SHA256 = hash(h)
	return h, h.Validate()
}

// Validate checks compact metadata and its hash. Full source/result validation
// belongs to MakeHistory and targeted Attempt.Validate, not to summary hashes.
func (h History) Validate() error {
	if h.Schema != HistorySchema || !receiverID.MatchString(h.ReceiverID) || h.Authority != "recorded_extraction_history_only" || h.QualificationCredit || h.Attempts == nil || len(h.Attempts) > MaxAttempts || h.AttemptCount != len(h.Attempts) {
		return ErrInvalid
	}
	counts := map[string]int{"pending": 0, "completed": 0, "expired": 0}
	seen := map[string]bool{}
	for i, s := range h.Attempts {
		if !ID.MatchString(s.ID) || seen[s.ID] || !sha.MatchString(s.AttemptSHA256) || !sha.MatchString(s.ArticleSHA256) || !eventKey.MatchString(s.SourceID) || !eventKey.MatchString(s.ArticleID) || s.Revision < 1 || s.Revision > 1000000 || !eventKey.MatchString(s.EventKey) || !validAt(s.StartedAt) || !s.Deadline.Equal(s.StartedAt.Add(30*time.Second)) {
			return ErrInvalid
		}
		if i > 0 {
			p := h.Attempts[i-1]
			if s.StartedAt.Before(p.StartedAt) || s.StartedAt.Equal(p.StartedAt) && s.ID <= p.ID {
				return ErrInvalid
			}
		}
		seen[s.ID] = true
		if _, ok := counts[s.Status]; !ok {
			return ErrInvalid
		}
		if s.Status == "pending" {
			if !s.CompletedAt.IsZero() || s.Outcome != "" || s.ReasonCode != "" || s.ResultSHA256 != "" {
				return ErrInvalid
			}
		} else if !validAt(s.CompletedAt) || s.CompletedAt.Before(s.StartedAt) || (s.Status == "expired") != !s.CompletedAt.Before(s.Deadline) || !slices.Contains([]string{"extracted", "abstain"}, s.Outcome) || s.ReasonCode == "" || !sha.MatchString(s.ResultSHA256) {
			return ErrInvalid
		}
		counts[s.Status]++
	}
	expected := h.SHA256
	h.SHA256 = ""
	if !reflect.DeepEqual(counts, h.StatusCounts) || expected != hash(h) {
		return ErrInvalid
	}
	return nil
}
