// Package socialwatch retains service-owned watch configuration and explicitly
// unqualified social recordings. It issues no research dispatch or trading grant.
package socialwatch

import (
	"encoding/json"
	"errors"
	"git.subcult.tv/subculture-collective/mantik/internal/paperaccount"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const Schema = "mantik-owned-social-journal-v1"
const MaxEntries = 128
const MaxWatches = 32
const MaxOriginalBytes = 16 << 10
const MaxJournalBytes = 8 << 20

var ErrInvalid = errors.New("invalid social watch command or journal")
var ErrConflict = errors.New("social watch identity or policy conflict")
var ErrCapacity = errors.New("social watch history capacity exhausted")
var ErrNotFound = errors.New("social watch not found")
var receiverPattern = regexp.MustCompile(`^receiver-[a-zA-Z0-9_-]{16,64}$`)
var providerPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
var sourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)
var pin = regexp.MustCompile(`^[a-f0-9]{64}$`)
var symbolPattern = regexp.MustCompile(`^[A-Z][A-Z0-9.-]{0,15}$`)

type Policy struct {
	Follow                     *FollowBounds `json:"follow,omitempty"`
	ID                         string        `json:"id"`
	Provider                   string        `json:"provider"`
	AccountID                  string        `json:"account_id"`
	Symbols                    []string      `json:"symbols"`
	Behavior                   string        `json:"behavior"`
	RuleID                     string        `json:"rule_id"`
	MaxObservationDelaySeconds int64         `json:"max_observation_delay_seconds"`
	Enabled                    bool          `json:"enabled"`
}

func validSymbols(a []string, required bool) bool {
	if len(a) > 16 || (required && len(a) == 0) || a == nil || !slices.IsSorted(a) {
		return false
	}
	for i, s := range a {
		if !symbolPattern.MatchString(s) || i > 0 && a[i-1] == s {
			return false
		}
	}
	return true
}
func (p Policy) Validate() error {
	if !paperaccount.ID.MatchString(p.ID) || !providerPattern.MatchString(p.Provider) || !sourceID.MatchString(p.AccountID) || !validSymbols(p.Symbols, true) || p.MaxObservationDelaySeconds < 1 || p.MaxObservationDelaySeconds > 86400 {
		return ErrInvalid
	}
	if p.Behavior != "bounded_paper_follow" && p.Follow != nil {
		return ErrInvalid
	}
	switch p.Behavior {
	case "bounded_paper_follow":
		if p.RuleID != "" || p.Follow == nil || !p.Follow.validate() {
			return ErrInvalid
		}
	case "research", "decision_review":
		if p.RuleID != "" {
			return ErrInvalid
		}
	case "configured_rule":
		if !paperaccount.ID.MatchString(p.RuleID) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

type PolicyCommand struct {
	ID             string `json:"id"`
	ReceiverID     string `json:"receiver_id"`
	PreviousSHA256 string `json:"previous_sha256"`
	Policy         Policy `json:"policy"`
}

func (c PolicyCommand) Validate() error {
	if !paperaccount.ID.MatchString(c.ID) || !receiverPattern.MatchString(c.ReceiverID) || (c.PreviousSHA256 != "" && !pin.MatchString(c.PreviousSHA256)) || c.Policy.Validate() != nil {
		return ErrInvalid
	}
	return nil
}

type SourceRef struct {
	Provider  string `json:"provider"`
	AccountID string `json:"account_id"`
	PostID    string `json:"post_id"`
}

func (r SourceRef) valid() bool {
	return providerPattern.MatchString(r.Provider) && sourceID.MatchString(r.AccountID) && sourceID.MatchString(r.PostID)
}

// Original is a Mantik recording manifest, not a platform-authenticated API
// response. Provider/account fields are source assertions, never attestation.
type Original struct {
	Activity   *Activity  `json:"activity,omitempty"`
	Schema     string     `json:"schema"`
	Source     SourceRef  `json:"source"`
	Revision   int64      `json:"revision"`
	Kind       string     `json:"kind"`
	OccurredAt time.Time  `json:"occurred_at"`
	ObservedAt time.Time  `json:"observed_at"`
	Text       string     `json:"text"`
	Symbols    []string   `json:"symbols"`
	RepostOf   *SourceRef `json:"repost_of"`
}

func (o Original) Validate() error {
	if (o.Schema != "mantik-social-recording-v1" && o.Schema != CopyRecordingSchema) || !o.Source.valid() || o.Revision < 1 || o.Revision > 1000000 || !validTime(o.OccurredAt) || !validTime(o.ObservedAt) || o.ObservedAt.Before(o.OccurredAt) || !utf8.ValidString(o.Text) || len(o.Text) > 8192 || !validSymbols(o.Symbols, false) {
		return ErrInvalid
	}
	if o.Schema == "mantik-social-recording-v1" && o.Activity != nil {
		return ErrInvalid
	}
	if o.Schema == CopyRecordingSchema {
		if o.Kind != "post" && o.Kind != "edit" && o.Kind != "delete" {
			return ErrInvalid
		}
		if o.Kind == "delete" {
			if o.Activity != nil {
				return ErrInvalid
			}
		} else if o.Activity == nil || !o.Activity.validate() || len(o.Symbols) != 1 || o.Symbols[0] != o.Activity.Symbol {
			return ErrInvalid
		}
	}
	switch o.Kind {
	case "post", "edit", "forecast", "declared_position":
		if strings.TrimSpace(o.Text) == "" || o.RepostOf != nil {
			return ErrInvalid
		}
	case "delete":
		if o.Text != "" || len(o.Symbols) != 0 || o.RepostOf != nil {
			return ErrInvalid
		}
	case "repost":
		if o.RepostOf == nil || !o.RepostOf.valid() || *o.RepostOf == o.Source {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
func validTime(t time.Time) bool { return !t.IsZero() && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999 }

type CaptureCommand struct {
	RequestResearch bool   `json:"request_research,omitempty"`
	ID              string `json:"id"`
	ReceiverID      string `json:"receiver_id"`
	WatchID         string `json:"watch_id"`
	PolicySHA256    string `json:"policy_sha256"`
	EvidenceClass   string `json:"evidence_class"`
	Original        string `json:"original"`
	OriginalSHA256  string `json:"original_sha256"`
}

func (c CaptureCommand) Decode() (Original, error) {
	var o Original
	if !paperaccount.ID.MatchString(c.ID) || !receiverPattern.MatchString(c.ReceiverID) || !paperaccount.ID.MatchString(c.WatchID) || !pin.MatchString(c.PolicySHA256) || !slices.Contains([]string{"synthetic_unqualified", "recorded_social_unqualified", "recorded_copy_unqualified"}, c.EvidenceClass) || len(c.Original) > MaxOriginalBytes || !utf8.ValidString(c.Original) || workerplan.Digest([]byte(c.Original)) != c.OriginalSHA256 || workerplan.ReadBoundedShape([]byte(c.Original), &o, MaxOriginalBytes) != nil || o.Validate() != nil {
		return o, ErrInvalid
	}
	if o.Schema == CopyRecordingSchema && c.EvidenceClass == "recorded_social_unqualified" || o.Schema != CopyRecordingSchema && c.EvidenceClass == "recorded_copy_unqualified" {
		return o, ErrInvalid
	}
	return o, nil
}

type PolicyRecord struct {
	Command      PolicyCommand `json:"command"`
	Version      int           `json:"version"`
	RegisteredAt time.Time     `json:"registered_at"`
	SHA256       string        `json:"sha256"`
}
type CaptureRecord struct {
	ResearchRequest           *ResearchRequest `json:"research_request,omitempty"`
	ResearchRequestDuplicate  bool             `json:"research_request_duplicate,omitempty"`
	Copy                      *CopyObservation `json:"copy,omitempty"`
	Command                   CaptureCommand   `json:"command"`
	Source                    Original         `json:"source"`
	RetainedAt                time.Time        `json:"retained_at"`
	SourceSemanticSHA256      string           `json:"source_semantic_sha256"`
	Root                      SourceRef        `json:"root"`
	ClaimGroupSHA256          string           `json:"claim_group_sha256"`
	SourceAccepted            bool             `json:"source_accepted"`
	SourceDuplicate           bool             `json:"source_duplicate"`
	SourceStatus              string           `json:"source_status"`
	PolicyStatus              string           `json:"policy_status"`
	ObservationDelaySeconds   int64            `json:"observation_delay_seconds"`
	AvailabilityDelaySeconds  int64            `json:"availability_delay_seconds"`
	Authority                 string           `json:"authority"`
	ProviderAccessVerified    bool             `json:"provider_access_verified"`
	InstrumentMappingVerified bool             `json:"instrument_mapping_verified"`
	IndependentEvidenceCredit bool             `json:"independent_evidence_credit"`
	DispatchAvailable         bool             `json:"dispatch_available"`
	AdmissionAvailable        bool             `json:"admission_available"`
	ExecutionAvailable        bool             `json:"execution_available"`
	QualificationCredit       bool             `json:"qualification_credit"`
	SHA256                    string           `json:"sha256"`
}
type Command struct {
	Policy  *PolicyCommand  `json:"policy"`
	Capture *CaptureCommand `json:"capture"`
}

func (c Command) identity() (string, string, error) {
	if (c.Policy == nil) == (c.Capture == nil) {
		return "", "", ErrInvalid
	}
	if c.Policy != nil {
		return c.Policy.ID, c.Policy.ReceiverID, c.Policy.Validate()
	}
	_, err := c.Capture.Decode()
	return c.Capture.ID, c.Capture.ReceiverID, err
}

type Entry struct {
	ResearchRequestMethod string         `json:"research_request_method,omitempty"`
	Sequence              int            `json:"sequence"`
	PreviousSHA256        string         `json:"previous_sha256"`
	At                    time.Time      `json:"at"`
	Command               Command        `json:"command"`
	Policy                *PolicyRecord  `json:"policy"`
	Capture               *CaptureRecord `json:"capture"`
	SHA256                string         `json:"sha256"`
}
type Journal struct {
	Schema     string  `json:"schema"`
	ReceiverID string  `json:"receiver_id"`
	Entries    []Entry `json:"entries"`
}
type Receipt struct {
	Entry     Entry `json:"entry"`
	Duplicate bool  `json:"duplicate"`
}
type WatchView struct {
	Policy                 PolicyRecord `json:"policy"`
	ConnectorStatus        string       `json:"connector_status"`
	ConnectorScope         SourceRef    `json:"connector_scope"`
	ProviderAccessVerified bool         `json:"provider_access_verified"`
	DispatchAvailable      bool         `json:"dispatch_available"`
}
type Catalog struct {
	ResearchRequests []ResearchRequest `json:"research_requests,omitempty"`
	ReceiverID       string            `json:"receiver_id"`
	HeadSHA256       string            `json:"head_sha256"`
	Watches          []WatchView       `json:"watches"`
	History          []Entry           `json:"history"`
}
type projection struct {
	research map[string]ResearchRequest
	policies map[string]PolicyRecord
	sources  map[string]CaptureRecord
	versions map[string]CaptureRecord
	last     time.Time
	head     string
}

func newProjection() projection {
	return projection{research: map[string]ResearchRequest{}, policies: map[string]PolicyRecord{}, sources: map[string]CaptureRecord{}, versions: map[string]CaptureRecord{}}
}
func clone[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}
func New(receiver string) (Journal, error) {
	if !receiverPattern.MatchString(receiver) {
		return Journal{}, ErrInvalid
	}
	return Journal{Schema: Schema, ReceiverID: receiver, Entries: []Entry{}}, nil
}
func sourceKey(s SourceRef) string { return workerplan.Hash(s) }
func versionKey(o Original) string {
	return workerplan.Hash(struct {
		Source   SourceRef
		Revision int64
	}{o.Source, o.Revision})
}
func semantic(o Original) string { o.ObservedAt = time.Time{}; return workerplan.Hash(o) }
func group(o Original) string {
	if o.Activity != nil {
		return copyGroup(o)
	}
	return workerplan.Hash(struct {
		Text    string
		Symbols []string
	}{strings.ToLower(strings.Join(strings.Fields(o.Text), " ")), o.Symbols})
}
func (p *projection) policy(c PolicyCommand, at time.Time) (PolicyRecord, error) {
	old, exists := p.policies[c.Policy.ID]
	if !exists {
		if c.PreviousSHA256 != "" {
			return PolicyRecord{}, ErrConflict
		}
		if len(p.policies) >= MaxWatches {
			return PolicyRecord{}, ErrCapacity
		}
	}
	if exists && (c.PreviousSHA256 != old.SHA256 || old.Command.Policy.Provider != c.Policy.Provider || old.Command.Policy.AccountID != c.Policy.AccountID) {
		return PolicyRecord{}, ErrConflict
	}
	for id, r := range p.policies {
		if id != c.Policy.ID && r.Command.Policy.Provider == c.Policy.Provider && r.Command.Policy.AccountID == c.Policy.AccountID {
			return PolicyRecord{}, ErrConflict
		}
	}
	r := PolicyRecord{Command: clone(c), Version: old.Version + 1, RegisteredAt: at}
	r.SHA256 = workerplan.Hash(r)
	p.policies[c.Policy.ID] = r
	return r, nil
}
func (p *projection) capture(c CaptureCommand, at time.Time) (CaptureRecord, error) {
	o, err := c.Decode()
	if err != nil {
		return CaptureRecord{}, err
	}
	watch, exists := p.policies[c.WatchID]
	if !exists {
		return CaptureRecord{}, ErrNotFound
	}
	w := watch.Command.Policy
	if watch.SHA256 != c.PolicySHA256 || o.Source.Provider != w.Provider || o.Source.AccountID != w.AccountID {
		return CaptureRecord{}, ErrConflict
	}
	if o.ObservedAt.After(at) {
		return CaptureRecord{}, ErrInvalid
	}
	r := CaptureRecord{Command: clone(c), Source: o, RetainedAt: at, SourceSemanticSHA256: semantic(o), Root: o.Source, ClaimGroupSHA256: group(o), SourceStatus: "retained", PolicyStatus: "dispatch_unbound", ObservationDelaySeconds: int64(o.ObservedAt.Sub(o.OccurredAt) / time.Second), AvailabilityDelaySeconds: int64(at.Sub(o.OccurredAt) / time.Second), Authority: "recorded_social_source_assertion_only"}
	r.Copy = p.copyObservation(o, w)
	if o.Schema == CopyRecordingSchema {
		r.Authority = "recorded_copy_source_assertion_only"
	}
	old, seen := p.versions[versionKey(o)]
	previous, hasPrevious := p.sources[sourceKey(o.Source)]
	switch {
	case seen && old.SourceSemanticSHA256 != r.SourceSemanticSHA256:
		r.SourceStatus = "source_revision_conflict"
	case seen:
		r.SourceStatus = "duplicate_source_revision"
		r.SourceDuplicate = true
		r.Root = old.Root
		r.ClaimGroupSHA256 = old.ClaimGroupSHA256
	case !hasPrevious && (o.Revision != 1 || o.Kind == "edit" || o.Kind == "delete"):
		r.SourceStatus = "unresolved_revision"
	case hasPrevious && (previous.Source.Schema != o.Schema || (o.Schema == CopyRecordingSchema && previous.Command.EvidenceClass != c.EvidenceClass) || (o.Activity != nil && previous.Source.Activity != nil && o.Activity.Kind != previous.Source.Activity.Kind) || previous.Source.Kind == "delete" || o.Revision != previous.Source.Revision+1 || (o.Kind != "edit" && o.Kind != "delete") || o.OccurredAt.Before(previous.Source.OccurredAt) || o.ObservedAt.Before(previous.Source.ObservedAt)):
		r.SourceStatus = "invalid_revision_transition"
	default:
		r.SourceAccepted = true
		if hasPrevious {
			r.Root = previous.Root
		}
		if o.Kind == "delete" {
			r.ClaimGroupSHA256 = previous.ClaimGroupSHA256
		}
		if o.Kind == "repost" {
			parent, ok := p.sources[sourceKey(*o.RepostOf)]
			if !ok || parent.Source.Kind == "delete" {
				r.SourceAccepted = false
				r.SourceStatus = "unresolved_repost"
			} else {
				r.Root = parent.Root
				r.ClaimGroupSHA256 = parent.ClaimGroupSHA256
			}
		}
	}
	switch {
	case r.SourceStatus != "retained":
		r.PolicyStatus = r.SourceStatus
	case !w.Enabled:
		r.PolicyStatus = "watch_disabled"
	case at.Sub(o.OccurredAt) > time.Duration(w.MaxObservationDelaySeconds)*time.Second:
		r.PolicyStatus = "observation_delay_exceeded"
	case o.Kind == "delete":
		r.PolicyStatus = "source_deleted"
	case !intersects(w.Symbols, o.Symbols):
		r.PolicyStatus = "instrument_not_watched_or_unresolved"
	case w.Behavior == "bounded_paper_follow" && o.Activity == nil:
		r.PolicyStatus = "copy_activity_required"
	case o.Activity != nil && slices.Contains([]string{"retracted", "reversed", "cancelled", "rejected"}, o.Activity.State) || o.Activity != nil && o.Activity.Kind == "observed_holding" && o.Activity.Side == "flat":
		r.PolicyStatus = "source_activity_terminal_or_reversed"
	case o.Activity != nil:
		r.PolicyStatus = "copy_source_mapping_and_admission_unavailable"
	case o.Kind == "forecast" || o.Kind == "declared_position":
		r.PolicyStatus = "structured_extraction_unavailable"
	}
	if c.RequestResearch {
		if !r.SourceAccepted || r.PolicyStatus != "dispatch_unbound" || w.Behavior != "research" || o.Activity != nil {
			return CaptureRecord{}, ErrConflict
		}
		requestKey := workerplan.Hash(struct{ ReceiverID, EvidenceClass, ClaimGroupSHA256 string }{c.ReceiverID, c.EvidenceClass, r.ClaimGroupSHA256})
		request, duplicate := p.research[requestKey]
		if !duplicate {
			request = ResearchRequest{Schema: "mantik-owned-social-research-request-v1", ID: "social-research-" + requestKey,
				ReceiverID: c.ReceiverID, WatchID: c.WatchID, PolicySHA256: c.PolicySHA256,
				CaptureID: c.ID, OriginalSHA256: c.OriginalSHA256, SourceSemanticSHA256: r.SourceSemanticSHA256,
				ClaimGroupSHA256: r.ClaimGroupSHA256, Source: o.Source, Root: r.Root, RequestedAt: at,
				EvidenceClass: c.EvidenceClass, Status: "pending_research", Authority: "service_owned_recorded_source_research_request_only"}
			request.SHA256 = workerplan.Hash(request)
			p.research[requestKey] = request
		}
		r.ResearchRequest = &request
		r.ResearchRequestDuplicate = duplicate
	}
	r.SHA256 = workerplan.Hash(r)
	if r.SourceAccepted {
		p.sources[sourceKey(o.Source)] = r
		p.versions[versionKey(o)] = r
	}
	return r, nil
}
func intersects(a, b []string) bool {
	for _, s := range a {
		if slices.Contains(b, s) {
			return true
		}
	}
	return false
}
func (p *projection) apply(receiver string, c Command, at time.Time, seq int, researchMethod string) (Entry, error) {
	_, owner, err := c.identity()
	if err != nil {
		return Entry{}, err
	}
	if owner != receiver {
		return Entry{}, ErrConflict
	}
	if !validTime(at) || at.Before(p.last) {
		return Entry{}, ErrInvalid
	}
	at = at.UTC()
	e := Entry{ResearchRequestMethod: researchMethod, Sequence: seq, PreviousSHA256: p.head, At: at, Command: clone(c)}
	if c.Policy != nil {
		v, err := p.policy(*c.Policy, at)
		if err != nil {
			return Entry{}, err
		}
		e.Policy = &v
	} else {
		v, err := p.capture(*c.Capture, at)
		if err != nil {
			return Entry{}, err
		}
		e.Capture = &v
	}
	e.SHA256 = workerplan.Hash(e)
	p.head = e.SHA256
	p.last = at
	return e, nil
}
func replay(j Journal) (projection, error) {
	p := newProjection()
	if j.Schema != Schema || !receiverPattern.MatchString(j.ReceiverID) || j.Entries == nil || len(j.Entries) > MaxEntries {
		return p, ErrInvalid
	}
	raw, err := json.Marshal(j)
	if err != nil || len(raw) > MaxJournalBytes {
		return p, ErrCapacity
	}
	ids := map[string]bool{}
	for i, e := range j.Entries {
		id, _, err := e.Command.identity()
		if err != nil || ids[id] {
			return p, ErrInvalid
		}
		ids[id] = true
		expected, err := p.apply(j.ReceiverID, e.Command, e.At, i+1, e.ResearchRequestMethod)
		if err != nil || !reflect.DeepEqual(expected, e) {
			return p, ErrInvalid
		}
		prefix := j
		prefix.Entries = j.Entries[:i+1]
		if validateResearchRequestSource(prefix, e) != nil {
			return p, ErrInvalid
		}
	}
	return p, nil
}
func Read(j Journal) (Catalog, error) {
	p, err := replay(j)
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{ReceiverID: j.ReceiverID, HeadSHA256: p.head, Watches: []WatchView{}, History: clone(j.Entries)}
	for _, r := range p.policies {
		out.Watches = append(out.Watches, WatchView{Policy: r, ConnectorStatus: "connector_not_configured", ConnectorScope: SourceRef{Provider: r.Command.Policy.Provider, AccountID: r.Command.Policy.AccountID}})
	}
	for _, request := range p.research {
		out.ResearchRequests = append(out.ResearchRequests, clone(request))
	}
	slices.SortFunc(out.ResearchRequests, func(a, b ResearchRequest) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(out.Watches, func(a, b WatchView) int {
		return strings.Compare(a.Policy.Command.Policy.ID, b.Policy.Command.Policy.ID)
	})
	return out, nil
}
func Append(j Journal, c Command, now func() time.Time) (Journal, Receipt, error) {
	p, err := replay(j)
	if err != nil {
		return Journal{}, Receipt{}, err
	}
	id, owner, err := c.identity()
	if err != nil {
		return Journal{}, Receipt{}, err
	}
	if owner != j.ReceiverID {
		return Journal{}, Receipt{}, ErrConflict
	}
	for _, e := range j.Entries {
		old, _, _ := e.Command.identity()
		if old == id {
			if !reflect.DeepEqual(c, e.Command) {
				return Journal{}, Receipt{}, ErrConflict
			}
			return clone(j), Receipt{Entry: clone(e), Duplicate: true}, nil
		}
	}
	if len(j.Entries) >= MaxEntries {
		return Journal{}, Receipt{}, ErrCapacity
	}
	if now == nil {
		return Journal{}, Receipt{}, ErrInvalid
	}
	method := ""
	if c.Capture != nil && c.Capture.RequestResearch {
		method = researchRequestSourceMethod
	}
	e, err := p.apply(j.ReceiverID, c, now(), len(j.Entries)+1, method)
	if err != nil {
		return Journal{}, Receipt{}, err
	}
	next := clone(j)
	next.Entries = append(next.Entries, e)
	if err := validateResearchRequestSource(next, e); err != nil {
		return Journal{}, Receipt{}, err
	}
	raw, err := json.Marshal(next)
	if err != nil || len(raw) > MaxJournalBytes {
		return Journal{}, Receipt{}, ErrCapacity
	}
	return next, Receipt{Entry: clone(e)}, nil
}
