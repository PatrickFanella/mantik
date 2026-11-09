package storage

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/alpacarecord"
	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/routing"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const MaxFeedRetentionBytes = 2 << 20

// A retirement names the actual paper checkpoint verified before the owner
// removes sources. Kept hashes distinguish intentional retirement from loss of
// a source which ongoing coverage or routing still owns. Prefixes keep the
// original registration digest rules across successive bounded segments.
type FeedRetention struct {
	Schema              string                    `json:"schema"`
	ReceiverID          string                    `json:"receiver_id"`
	Sequence            int64                     `json:"sequence"`
	PreviousSHA256      string                    `json:"previous_sha256"`
	Checkpoint          PaperFeedCheckpointHeader `json:"checkpoint"`
	Prefixes            []PaperFeedPrefix         `json:"prefixes"`
	Kept                []FeedKeptSource          `json:"kept"`
	WorkingSourceSHA256 string                    `json:"working_source_sha256"`
	SHA256              string                    `json:"sha256"`
}

type FeedKeptSource struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

func (r FeedRetention) Validate() error {
	sha := r.SHA256
	r.SHA256 = ""
	if r.Schema != "mantik-feed-retention-v1" || !ValidReceiverID(r.ReceiverID) || r.Sequence < 1 ||
		r.Sequence == 1 && r.PreviousSHA256 != "" || r.Sequence > 1 && !decisionPin.MatchString(r.PreviousSHA256) ||
		r.Checkpoint.Validate() != nil || r.Checkpoint.ReceiverID != r.ReceiverID || r.Prefixes == nil || r.Kept == nil ||
		len(r.Prefixes) != r.Checkpoint.Archive.Registrations || workerplan.Hash(r.Prefixes) != r.Checkpoint.PrefixesSHA256 ||
		len(r.Kept) > 2*MaxFeedEvidence+8192 || !decisionPin.MatchString(r.WorkingSourceSHA256) || workerplan.Hash(r) != sha {
		return ErrFeedArchiveConflict
	}
	previous := ""
	for _, p := range r.Kept {
		key := archivedSourceKey(p.Kind, p.ID)
		if key <= previous || !validArchivedSourceKey(p.Kind, p.ID) || !decisionPin.MatchString(p.SHA256) {
			return ErrFeedArchiveConflict
		}
		previous = key
	}
	previous = ""
	for _, p := range r.Prefixes {
		if !decisionRegistration.MatchString(p.RegistrationID) || p.RegistrationID <= previous {
			return ErrFeedArchiveConflict
		}
		previous = p.RegistrationID
	}
	return nil
}

func (r *FeedRetention) prefix(registration string) PaperDecisionFeedPrefix {
	if r == nil {
		return PaperDecisionFeedPrefix{}
	}
	for _, p := range r.Prefixes {
		if p.RegistrationID == registration {
			return p.Prefix
		}
	}
	return PaperDecisionFeedPrefix{}
}

// The metadata is only installed by the source owner with its actual checkpoint.
// No basis or HTTP input can opt into retired-source replay.
type feedCustodyView struct {
	err        error
	checkpoint PaperFeedCheckpoint
	seals      map[string]PaperFeedSourcePin
}

func (v *feedCustodyView) pin(kind, id string) (PaperFeedSourcePin, bool) {
	if v == nil {
		return PaperFeedSourcePin{}, false
	}
	key := kind + ":" + id
	i, ok := slices.BinarySearchFunc(v.checkpoint.Records, key, func(p PaperFeedSourcePin, k string) int { return strings.Compare(p.Kind+":"+p.ID, k) })
	if ok {
		return v.checkpoint.Records[i], true
	}
	p, ok := v.seals[key]
	if ok && !p.At.After(v.checkpoint.CutAt) {
		return PaperFeedSourcePin{}, false
	}
	return p, ok
}

func feedSourceHash(kind, id string, feed FeedEvidenceSnapshot) (string, bool) {
	var value any
	switch kind {
	case "publication":
		p, ok := feed.Records[id]
		if !ok {
			return "", false
		}
		raw, err := canonicalRecording(p.Recording)
		if err != nil {
			return "", false
		}
		p.Recording = json.RawMessage(raw)
		value = p
	case "admission":
		p, ok := feed.Collectors.Admissions[id]
		if !ok {
			return "", false
		}
		value = p
	case "command":
		p, ok := feed.Collectors.Commands[id]
		if !ok {
			return "", false
		}
		value = p
	case "input":
		if feed.Routing == nil {
			return "", false
		}
		p, ok := feed.Routing.Inputs[id]
		if !ok {
			return "", false
		}
		value = p
	default:
		return "", false
	}
	return workerplan.Hash(value), true
}

func validateRetainedFeed(feed FeedEvidenceSnapshot) error {
	r := feed.Retention
	if r == nil {
		if feed.Collectors.ArchiveSHA256 != "" {
			return ErrFeedArchiveConflict
		}
		return nil
	}
	if r.Validate() != nil || r.ReceiverID != feed.ReceiverID || feed.Collectors.ArchiveSHA256 != r.SHA256 {
		return ErrFeedArchiveConflict
	}
	kept := map[string]bool{}
	for _, p := range r.Kept {
		sha, ok := feedSourceHash(p.Kind, p.ID, feed)
		if !ok || sha != p.SHA256 {
			return ErrFeedArchiveConflict
		}
		kept[archivedSourceKey(p.Kind, p.ID)] = true
	}
	cut := r.Checkpoint.CutAt
	for id, p := range feed.Records {
		if !p.PublishedAt.After(cut) && !kept[archivedSourceKey("publication", id)] {
			return ErrFeedArchiveConflict
		}
	}
	for id, p := range feed.Collectors.Admissions {
		if !p.At.After(cut) && !kept[archivedSourceKey("admission", id)] {
			return ErrFeedArchiveConflict
		}
	}
	for id, p := range feed.Collectors.Commands {
		if !p.Receipt.At.After(cut) && !kept[archivedSourceKey("command", id)] {
			return ErrFeedArchiveConflict
		}
	}
	if feed.Routing != nil {
		for id, p := range feed.Routing.Inputs {
			_, collectorOwned := feed.Collectors.Admissions[id]
			if collectorOwned && !p.Input.ReceivedAt.After(cut) && !kept[archivedSourceKey("input", id)] {
				return ErrFeedArchiveConflict
			}
		}
	}
	return nil
}

// Keep the current coverage window (including receipt-time ties and incomplete
// publications), current lease proof, and every live routing reference. Archive
// custody handles completed paper bases independently of this live working set.
func retainedFeedSources(feed FeedEvidenceSnapshot, checkpoint PaperFeedCheckpoint, previous *FeedRetention) (FeedEvidenceSnapshot, *FeedRetention, error) {
	if checkpoint.Validate() != nil {
		return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
	}
	return planRetainedFeed(feed, checkpoint.PaperFeedCheckpointHeader, checkpoint.Prefixes, previous)
}

func planRetainedFeed(feed FeedEvidenceSnapshot, checkpoint PaperFeedCheckpointHeader, prefixes []PaperFeedPrefix, previous *FeedRetention) (FeedEvidenceSnapshot, *FeedRetention, error) {
	canonical, err := canonicalArchiveSource(feed)
	if err != nil || checkpoint.Validate() != nil || checkpoint.ReceiverID != feed.ReceiverID || workerplan.Hash(canonical) != checkpoint.Archive.SourceSHA256 {
		return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
	}
	index := newFeedPrefixIndex(feed)
	expectedPrefixes := []PaperFeedPrefix{}
	for id := range feed.Collectors.Entries {
		expectedPrefixes = append(expectedPrefixes, PaperFeedPrefix{id, index.at(id, checkpoint.CutAt)})
	}
	slices.SortFunc(expectedPrefixes, func(a, b PaperFeedPrefix) int { return strings.Compare(a.RegistrationID, b.RegistrationID) })
	if !reflect.DeepEqual(expectedPrefixes, prefixes) || workerplan.Hash(expectedPrefixes) != checkpoint.PrefixesSHA256 {
		return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
	}
	out := clone(feed)
	out.custody = nil
	keepRecords, keepAdmissions, keepInputs, keepCommands := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for id, e := range feed.Collectors.Entries {
		records, admissions := index.window(id, checkpoint.CutAt, time.Time{})
		for key := range records {
			keepRecords[key] = true
		}
		for key := range admissions {
			keepAdmissions[key], keepInputs[key] = true, true
		}
		if e.Lease != nil {
			best := ""
			for key, c := range feed.Collectors.Commands {
				if c.Command.RegistrationID == id && reflect.DeepEqual(c.Receipt.Entry, e) && (best == "" || key < best) {
					best = key
				}
			}
			if best == "" {
				return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
			}
			keepCommands[best] = true
		}
	}
	// Preserve admissions awaiting their raw publication. A late publication
	// must still validate against its exact original normalized inputs.
	published := map[string]bool{}
	unknown := map[string]bool{}
	for _, record := range feed.Records {
		ids, err := publishedFeedInputIDs(record.Recording)
		if err != nil {
			unknown[record.RegistrationID] = true
			continue
		}
		for _, id := range ids {
			published[id] = true
		}
	}
	for id, admission := range feed.Collectors.Admissions {
		if unknown[admission.Fence.RegistrationID] || !published[id] {
			keepAdmissions[id], keepInputs[id] = true, true
		}
	}
	if feed.Routing != nil {
		for _, rt := range feed.Routing.Runtime {
			for _, f := range rt.Facts {
				if f.InputRef != "" {
					keepInputs[f.InputRef] = true
				}
			}
		}
		for _, c := range feed.Routing.Cases {
			if c.LastInputRef != "" {
				keepInputs[c.LastInputRef] = true
			}
		}
		// Inputs owned outside the collector keep their existing routing owner.
		for id := range feed.Routing.Inputs {
			if _, owned := feed.Collectors.Admissions[id]; !owned {
				keepInputs[id] = true
			}
		}
	}
	for id := range keepInputs {
		if feed.Routing == nil {
			return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
		}
		if _, exists := feed.Routing.Inputs[id]; !exists {
			return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
		}
	}
	for id := range out.Records {
		if !keepRecords[id] {
			delete(out.Records, id)
		}
	}
	for id := range out.Collectors.Commands {
		if !keepCommands[id] {
			delete(out.Collectors.Commands, id)
		}
	}
	for id := range out.Collectors.Admissions {
		if !keepAdmissions[id] && !keepInputs[id] {
			delete(out.Collectors.Admissions, id)
		}
	}
	if out.Routing != nil {
		for id := range out.Routing.Inputs {
			if !keepInputs[id] {
				delete(out.Routing.Inputs, id)
			}
		}
	}
	r := &FeedRetention{Schema: "mantik-feed-retention-v1", ReceiverID: feed.ReceiverID, Sequence: 1, Checkpoint: checkpoint, Prefixes: clone(prefixes), Kept: []FeedKeptSource{}}
	if previous != nil {
		if previous.Validate() != nil || previous.ReceiverID != feed.ReceiverID {
			return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
		}
		r.Sequence, r.PreviousSHA256 = previous.Sequence+1, previous.SHA256
	}
	add := func(kind, id string) {
		sha, _ := feedSourceHash(kind, id, out)
		r.Kept = append(r.Kept, FeedKeptSource{kind, id, sha})
	}
	for id := range out.Records {
		add("publication", id)
	}
	for id := range out.Collectors.Commands {
		add("command", id)
	}
	for id := range out.Collectors.Admissions {
		add("admission", id)
	}
	if out.Routing != nil {
		for id := range out.Routing.Inputs {
			add("input", id)
		}
	}
	slices.SortFunc(r.Kept, func(a, b FeedKeptSource) int {
		return strings.Compare(archivedSourceKey(a.Kind, a.ID), archivedSourceKey(b.Kind, b.ID))
	})
	r.WorkingSourceSHA256, err = feedWorkingSourceHash(out)
	if err != nil {
		return FeedEvidenceSnapshot{}, nil, err
	}
	r.SHA256 = workerplan.Hash(*r)
	out.Retention = r
	out.Collectors.ArchiveSHA256 = r.SHA256
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > MaxFeedRetentionBytes {
		return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveCapacity
	}
	if validateRetainedFeed(out) != nil {
		return FeedEvidenceSnapshot{}, nil, ErrFeedArchiveConflict
	}
	return out, r, nil
}

// Publication validation already replays the provider normalizer through the
// owner callback. Retirement only needs its stable input identities to separate
// published receipts from still-pending admissions; it does not issue coverage.
// Keep this lookup independent of the Alpaca journal/collector client package.
func publishedFeedInputIDs(raw json.RawMessage) ([]string, error) {
	var r alpacarecord.Recording
	if json.Unmarshal(raw, &r) != nil || alpacarecord.ValidateBase(r) != nil {
		return nil, ErrFeedArchiveConflict
	}
	ids := []string{}
	add := func(symbol, key string) {
		i := domain.RoutingInput{Source: "alpaca-" + strings.ReplaceAll(r.Feed, "_", "-"), Symbol: symbol, EvidenceClass: r.EvidenceClass, Provenance: &domain.InputProvenance{RecordKey: r.CaptureID + "-" + key, Revision: r.Revision}}
		ids = append(ids, routing.MarketInputID(i))
	}
	switch r.Capability {
	case "latest_quotes":
		for _, symbol := range r.Symbols {
			add(symbol, symbol+"-quote")
		}
	case "market_calendar":
		var days []struct {
			Date string `json:"date"`
		}
		if json.Unmarshal([]byte(r.Original), &days) != nil || days == nil || len(days) > 31 {
			return nil, ErrFeedArchiveConflict
		}
		for _, day := range days {
			for _, symbol := range r.Symbols {
				add(symbol, symbol+"-session-"+day.Date)
			}
		}
	default:
		return nil, ErrFeedArchiveConflict
	}
	return ids, nil
}

func feedWorkingSourceHash(feed FeedEvidenceSnapshot) (string, error) {
	feed = clone(feed)
	feed.Retention = nil
	feed.Collectors.ArchiveSHA256 = ""
	canonical, err := canonicalArchiveSource(feed)
	if err != nil {
		return "", err
	}
	return workerplan.Hash(canonical), nil
}

// Full audit independently rederives the keeper plan from original archived
// sources. A self-rehashed active cursor cannot authorize a different deletion.
func auditFeedRetention(feed FeedEvidenceSnapshot, original FeedArchive) error {
	r := feed.Retention
	if r == nil {
		return validateRetainedFeed(feed)
	}
	if original.FeedArchiveHeader != r.Checkpoint.Archive || original.Validate() != nil {
		return ErrFeedArchiveConflict
	}
	_, expected, err := planRetainedFeed(original.Source, r.Checkpoint, r.Prefixes, original.Source.Retention)
	if err != nil || !reflect.DeepEqual(expected, r) {
		return ErrFeedArchiveConflict
	}
	return validateFeedCustody(feed)
}

func decisionFeedSourcePin(v retainedPaperDecision) PaperFeedSourcePin {
	r := v.Record
	return PaperFeedSourcePin{"decision", r.Command.ID, r.Schema, r.SHA256, r.BasisSHA256, r.BasisSHA256, r.FinalizedAt, r.Command.ID, r.SHA256}
}
func outcomeFeedSourcePin(v retainedPaperOutcome) PaperFeedSourcePin {
	r := v.Record
	return PaperFeedSourcePin{"outcome", r.Command.ID, r.Schema, r.SHA256, r.BasisSHA256, workerplan.Hash(v.Basis.Observation), r.EvaluatedAt, r.Command.DecisionID, r.Command.DecisionSHA256}
}
func validateFeedCustody(feed FeedEvidenceSnapshot) error {
	if err := validateRetainedFeed(feed); err != nil {
		return err
	}
	if feed.Retention == nil {
		return nil
	}
	v := feed.custody
	if v == nil || v.err != nil || v.checkpoint.Validate() != nil || v.checkpoint.PaperFeedCheckpointHeader != feed.Retention.Checkpoint {
		return ErrFeedArchiveConflict
	}
	return nil
}
func verifyFeedSourcePin(feed FeedEvidenceSnapshot, pin PaperFeedSourcePin) error {
	if err := validateFeedCustody(feed); err != nil {
		return err
	}
	actual, ok := feed.custody.pin(pin.Kind, pin.ID)
	if !ok || actual != pin {
		return ErrFeedArchiveConflict
	}
	return nil
}

// A custody seal permits sources which actually left active state. It does not
// hide changed/missing working-set records or a changed unretired prefix.
func verifyRetiredPaperBasis(c PaperDecisionCommand, b PaperDecisionBasis, at time.Time, feed FeedEvidenceSnapshot) error {
	r := feed.Retention
	if r == nil {
		return ErrFeedArchiveConflict
	}
	if !at.Before(r.Checkpoint.CutAt) && b.Prefix != nil && *b.Prefix != newFeedPrefixIndex(feed).at(c.RegistrationID, at) {
		return ErrPaperDecisionConflict
	}
	kept := map[string]bool{}
	for _, p := range r.Kept {
		kept[archivedSourceKey(p.Kind, p.ID)] = true
	}
	for id, p := range b.Feed.Records {
		if active, exists := feed.Records[id]; exists {
			if !sameFeedRecord(p, active) {
				return ErrPaperDecisionConflict
			}
		} else if p.PublishedAt.After(r.Checkpoint.CutAt) || kept[archivedSourceKey("publication", id)] {
			return ErrPaperDecisionConflict
		}
	}
	for id, a := range b.Feed.Collectors.Admissions {
		if active, exists := feed.Collectors.Admissions[id]; exists {
			if !reflect.DeepEqual(a, active) || feed.Routing == nil || !reflect.DeepEqual(b.Feed.Routing.Inputs[id], feed.Routing.Inputs[id]) {
				return ErrPaperDecisionConflict
			}
		} else if a.At.After(r.Checkpoint.CutAt) || kept[archivedSourceKey("admission", id)] {
			return ErrPaperDecisionConflict
		}
		if feed.Routing != nil {
			if input, exists := feed.Routing.Inputs[id]; exists && !reflect.DeepEqual(b.Feed.Routing.Inputs[id], input) {
				return ErrPaperDecisionConflict
			}
		}
	}
	entry := b.Feed.Collectors.Entries[c.RegistrationID]
	if entry.Lease != nil && entry.Lease.UpdatedAt.After(r.Checkpoint.CutAt) {
		found := false
		for _, command := range feed.Collectors.Commands {
			if command.Command.RegistrationID == c.RegistrationID && !command.Receipt.At.After(at) && reflect.DeepEqual(command.Receipt.Entry, entry) {
				found = true
				break
			}
		}
		if !found {
			return ErrPaperDecisionConflict
		}
	}
	return nil
}
func (m *Memory) retainPaperFeedSourceSeal(pin PaperFeedSourcePin) {
	if m.feedRetention == nil {
		return
	}
	if m.paperFeedSourceSeals == nil {
		m.paperFeedSourceSeals = map[string]PaperFeedSourcePin{}
	}
	m.paperFeedSourceSeals[pin.Kind+":"+pin.ID] = pin
}
