package review

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/investigation"
	"git.subcult.tv/subculture-collective/mantik/internal/news"
	"git.subcult.tv/subculture-collective/mantik/internal/newscase"
	"git.subcult.tv/subculture-collective/mantik/internal/question"
)

const HistorySchema = "mantik-case-review-history-v1"

var HistoryCaseID = regexp.MustCompile(`^news-case-[a-f0-9]{64}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var receiverID = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var questionID = regexp.MustCompile(`^hypothesis-[a-f0-9]{64}$`)

// Summary binds a targeted attempt without copying its raw model artifacts,
// excerpts, news group or complete hypothesis inventory into the catalogue.
type Summary struct {
	ID                        string                `json:"id"`
	AttemptSHA256             string                `json:"attempt_sha256"`
	TypeID                    string                `json:"type_id"`
	ProfileSHA256             string                `json:"profile_sha256,omitempty"`
	HypothesisInventorySHA256 string                `json:"hypothesis_inventory_sha256,omitempty"`
	HypothesisCatalogueSHA256 string                `json:"hypothesis_catalogue_sha256,omitempty"`
	Status                    string                `json:"status"`
	StartedAt                 time.Time             `json:"started_at"`
	Deadline                  time.Time             `json:"deadline"`
	CompletedAt               time.Time             `json:"completed_at,omitzero"`
	Subject                   investigation.Subject `json:"subject"`
	EvidenceCurrent           bool                  `json:"evidence_current"`
	ReviewApplicable          bool                  `json:"review_applicable"`
	ResultID                  string                `json:"result_id,omitempty"`
	StepsSHA256               string                `json:"steps_sha256,omitempty"`
	Outcome                   string                `json:"outcome,omitempty"`
	ReasonCode                string                `json:"reason_code,omitempty"`
	ProposalReasons           []string              `json:"proposal_reasons"`
	Reuse                     *question.Ref         `json:"reuse,omitempty"`
	ArtifactCount             int                   `json:"artifact_count"`
	ToolCount                 int                   `json:"tool_count"`
	RejectedToolCount         int                   `json:"rejected_tool_count"`
	Checks                    []investigation.Check `json:"checks"`
}

type History struct {
	Schema              string         `json:"schema"`
	ReceiverID          string         `json:"receiver_id"`
	Case                newscase.Case  `json:"case"`
	Attempts            []Summary      `json:"attempts"`
	AttemptCount        int            `json:"attempt_count"`
	StatusCounts        map[string]int `json:"status_counts"`
	OutcomeCounts       map[string]int `json:"outcome_counts"`
	ReasonCounts        map[string]int `json:"reason_counts"`
	ArtifactCount       int            `json:"artifact_count"`
	RejectedToolCount   int            `json:"rejected_tool_count"`
	Authority           string         `json:"authority"`
	QualificationCredit bool           `json:"qualification_credit"`
	SHA256              string         `json:"sha256"`
}

func MakeHistory(receiver string, c newscase.Case, attempts []Attempt) (History, error) {
	if len(attempts) > MaxAttempts {
		return History{}, ErrCapacity
	}
	items := []Summary{}
	state := newscase.State{Cases: map[string]newscase.Case{c.ID: c}}
	for _, a := range attempts {
		contract, err := Definition(a)
		if err != nil || a.Schema != Schema || a.ModelMode != "recorded_model_fixture" {
			return History{}, ErrInvalid
		}
		groupRaw, err := json.Marshal(a.Pin.Group)
		if err != nil || news.SHA256(string(groupRaw)) != a.Pin.Subject.SnapshotSHA256 || a.Pin.Group.ID != a.Pin.Subject.CaseID || a.Pin.Group.EventKey != a.Pin.Subject.EventKey || a.Pin.Group.EvidenceClass != a.Pin.Subject.EvidenceClass {
			return History{}, ErrInvalid
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return History{}, ErrInvalid
		}
		view := Inspect(a, state)
		s := Summary{ID: a.ID, AttemptSHA256: news.SHA256(string(raw)), TypeID: contract.TypeID, Status: a.Status, StartedAt: a.StartedAt, Deadline: a.Deadline, CompletedAt: a.CompletedAt, Subject: a.Pin.Subject, EvidenceCurrent: view.EvidenceCurrent, ReviewApplicable: view.ReviewApplicable, Checks: []investigation.Check{}, ProposalReasons: []string{}}
		if a.Profile != nil {
			s.ProfileSHA256 = a.Profile.SHA256
		}
		if a.HypothesisInventory != nil {
			s.HypothesisInventorySHA256, s.HypothesisCatalogueSHA256 = a.HypothesisInventory.SHA256, a.HypothesisInventory.CatalogueSHA256
		}
		if a.Status == "pending" {
			if a.Result != nil || a.StepsSHA256 != "" || len(a.Steps) != 0 {
				return History{}, ErrInvalid
			}
		} else {
			if a.Result == nil {
				return History{}, ErrInvalid
			}
			r := *a.Result
			if !ResultMatchesProfile(a, r) || r.Status != "finalized" || r.Authority != "research_only_unqualified" || r.QualificationCredit || !reflect.DeepEqual(r.Subject, a.Pin.Subject) {
				return History{}, ErrInvalid
			}
			id := r.ID
			r.ID = ""
			raw, _ = json.Marshal(r)
			if id != "review-"+news.SHA256(string(raw)) {
				return History{}, ErrInvalid
			}
			steps := []json.RawMessage{}
			for _, step := range a.Steps {
				steps = append(steps, json.RawMessage(step))
			}
			_, sha, err := CanonicalSteps(steps)
			if err != nil || sha != a.StepsSHA256 {
				return History{}, ErrInvalid
			}
			s.ResultID, s.StepsSHA256, s.Outcome, s.ReasonCode = id, a.StepsSHA256, r.Outcome, r.ReasonCode
			s.ArtifactCount, s.ToolCount, s.Checks = len(r.Artifacts), len(r.Tools), r.Checks
			for _, tool := range r.Tools {
				if tool.Status == "rejected" {
					s.RejectedToolCount++
				}
			}
			if r.Proposal != nil {
				s.ProposalReasons = r.Proposal.ReasonCodes
				if r.Proposal.Candidate != nil {
					s.Reuse = r.Proposal.Candidate.Reuse
				}
			}
		}
		items = append(items, s)
	}
	return history(receiver, c, items)
}

func history(receiver string, c newscase.Case, items []Summary) (History, error) {
	if !receiverID.MatchString(receiver) || !HistoryCaseID.MatchString(c.ID) || c.ID != newscase.CaseID(c.EvidenceClass, c.EventKey) || c.Version < 1 || c.Generation < 1 || c.Behavior != "research" || c.Status != "open" && c.Status != "closed" || !validTime(c.OpenedAt) || !validTime(c.UpdatedAt) || c.UpdatedAt.Before(c.OpenedAt) || len(items) > MaxAttempts {
		return History{}, ErrInvalid
	}
	h := History{Schema: HistorySchema, ReceiverID: receiver, Case: c, Attempts: append([]Summary{}, items...), StatusCounts: map[string]int{"pending": 0, "completed": 0, "expired": 0, "superseded": 0}, OutcomeCounts: map[string]int{"abstain": 0, "request_evidence": 0, "propose_hypothesis": 0}, ReasonCounts: map[string]int{}, Authority: "recorded_review_attempt_history_only"}
	seen := map[string]bool{}
	for _, s := range items {
		p, err := investigation.ProfileFor(s.TypeID)
		if err != nil || !ID.MatchString(s.ID) || seen[s.ID] || !digest.MatchString(s.AttemptSHA256) || s.ProfileSHA256 != "" && s.ProfileSHA256 != p.SHA256 || s.ProfileSHA256 == "" && s.TypeID != investigation.TypeID || !validTime(s.StartedAt) || !s.Deadline.Equal(s.StartedAt.Add(time.Duration(p.Contract.TimeoutSeconds)*time.Second)) || s.Subject.ReceiverID != receiver || s.Subject.CaseID != c.ID || s.Subject.EventGroupID != c.ID || s.Subject.CaseVersion < 1 || s.Subject.CaseVersion > c.Version || s.Subject.CaseGeneration < 1 || s.Subject.CaseGeneration > c.Generation || s.Subject.EventKey != c.EventKey || s.Subject.EvidenceClass != c.EvidenceClass || !s.Subject.AsOf.Equal(s.StartedAt) || !digest.MatchString(s.Subject.SnapshotSHA256) {
			return History{}, ErrInvalid
		}
		seen[s.ID] = true
		if s.TypeID == investigation.ReuseTypeID {
			if !digest.MatchString(s.HypothesisInventorySHA256) || !digest.MatchString(s.HypothesisCatalogueSHA256) {
				return History{}, ErrInvalid
			}
		} else if s.HypothesisInventorySHA256 != "" || s.HypothesisCatalogueSHA256 != "" || s.Reuse != nil {
			return History{}, ErrInvalid
		}
		if _, ok := h.StatusCounts[s.Status]; !ok {
			return History{}, ErrInvalid
		}
		h.StatusCounts[s.Status]++
		current := c.Status == "open" && s.Subject.CaseVersion == c.Version && s.Subject.CaseGeneration == c.Generation
		if s.Status == "superseded" && current {
			return History{}, ErrInvalid
		}
		if s.EvidenceCurrent != current || s.ReviewApplicable != (current && s.Status == "completed") || s.Checks == nil || s.ProposalReasons == nil {
			return History{}, ErrInvalid
		}
		if s.Status == "pending" {
			if !s.CompletedAt.IsZero() || s.ResultID != "" || s.StepsSHA256 != "" || s.Outcome != "" || s.ReasonCode != "" || s.Reuse != nil || len(s.ProposalReasons) != 0 || len(s.Checks) != 0 || s.ArtifactCount != 0 || s.ToolCount != 0 || s.RejectedToolCount != 0 {
				return History{}, ErrInvalid
			}
		} else {
			if !validTime(s.CompletedAt) || s.CompletedAt.Before(s.StartedAt) || !strings.HasPrefix(s.ResultID, "review-") || !digest.MatchString(strings.TrimPrefix(s.ResultID, "review-")) || !digest.MatchString(s.StepsSHA256) || !ID.MatchString(s.ReasonCode) || s.ArtifactCount < 0 || s.ArtifactCount > p.Contract.MaxTurns || s.ToolCount < 0 || s.ToolCount > p.Contract.MaxToolCalls || s.RejectedToolCount < 0 || s.RejectedToolCount > s.ToolCount || len(s.Checks) != len(p.Contract.RequiredChecks) || len(s.ProposalReasons) > 4 {
				return History{}, ErrInvalid
			}
			if (s.Status == "expired") != (!s.CompletedAt.Before(s.Deadline)) {
				return History{}, ErrInvalid
			}
			if _, ok := h.OutcomeCounts[s.Outcome]; !ok {
				return History{}, ErrInvalid
			}
			h.OutcomeCounts[s.Outcome]++
			h.ReasonCounts[s.ReasonCode]++
			for _, reason := range s.ProposalReasons {
				if !ID.MatchString(reason) {
					return History{}, ErrInvalid
				}
			}
			for i, check := range s.Checks {
				if check.ID != p.Contract.RequiredChecks[i] || check.Version != "v1" || check.Passed != (check.Status == "passed") || check.Status != "passed" && check.Status != "failed" && check.Status != "not_run" {
					return History{}, ErrInvalid
				}
			}
			if s.Reuse != nil && (s.Outcome != "propose_hypothesis" || !questionID.MatchString(s.Reuse.ID) || !digest.MatchString(s.Reuse.DeclarationSHA256)) {
				return History{}, ErrInvalid
			}
		}
		h.ArtifactCount += s.ArtifactCount
		h.RejectedToolCount += s.RejectedToolCount
	}
	slices.SortFunc(h.Attempts, func(a, b Summary) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	h.AttemptCount = len(items)
	raw, _ := json.Marshal(h)
	h.SHA256 = news.SHA256(string(raw))
	raw, _ = json.Marshal(h)
	var detached History
	_ = json.Unmarshal(raw, &detached)
	return detached, nil
}

func (h History) Validate() error {
	if h.Attempts == nil {
		return ErrInvalid
	}
	expected, err := history(h.ReceiverID, h.Case, h.Attempts)
	if err != nil || !reflect.DeepEqual(expected, h) {
		return ErrInvalid
	}
	return nil
}
