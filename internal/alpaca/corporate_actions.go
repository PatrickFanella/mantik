package alpaca

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// CorporateActionFeed identifies a REST snapshot query, not an exchange feed.
const CorporateActionFeed = "us_rest_all_snapshot_v1"
const CorporateActionContract = "alpaca-us-rest-corporate-action-snapshot-v1"

// CorporateActionRecord is retained source material. Dates, rates and symbols
// inside OriginalJSON have not been validated for economic application.
type CorporateActionRecord struct {
	Kind         string `json:"kind"`
	SourceID     string `json:"source_id"`
	OriginalJSON string `json:"original_json"`
	SHA256       string `json:"sha256"`
}

// CorporateActionSnapshot describes only the page observed at ReceivedAt. A
// terminal page does not establish complete provider coverage or prior knowledge.
type CorporateActionSnapshot struct {
	PageToken                 string                  `json:"page_token,omitempty"`
	RequestedAt               *time.Time              `json:"requested_at,omitempty"`
	CaptureID                 string                  `json:"capture_id"`
	Revision                  int                     `json:"revision"`
	EvidenceClass             string                  `json:"evidence_class"`
	AccessRef                 string                  `json:"access_ref"`
	ReceivedAt                time.Time               `json:"received_at"`
	Contract                  string                  `json:"contract"`
	QueryDateBasis            string                  `json:"query_date_basis"`
	DataQuality               string                  `json:"data_quality"`
	Region                    string                  `json:"region"`
	Symbols                   []string                `json:"symbols"`
	Start                     string                  `json:"start"`
	End                       string                  `json:"end"`
	AvailableAt               time.Time               `json:"available_at"`
	RawRef                    string                  `json:"raw_ref"`
	SourceSHA256              string                  `json:"source_sha256"`
	Records                   []CorporateActionRecord `json:"records"`
	NextPageToken             *string                 `json:"next_page_token"`
	PageState                 string                  `json:"page_state"`
	CoverageComplete          bool                    `json:"coverage_complete"`
	HistoricalKnowledgeProven bool                    `json:"historical_knowledge_proven"`
	EconomicApplicationReady  bool                    `json:"economic_application_ready"`
}

// Reject duplicate keys instead of projecting a last-wins interpretation of
// ambiguous retained bytes. This also bounds nesting independently of byte size.
func corporateActionJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("corporate-action JSON nesting exceeds bound")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errors.New("invalid corporate-action JSON container")
	}
	keys := map[string]bool{}
	for d.More() {
		if delimiter == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("duplicate corporate-action JSON key")
			}
			keys[name] = true
		}
		if err := corporateActionJSONValue(d, depth+1); err != nil {
			return err
		}
	}
	end, err := d.Token()
	if err != nil {
		return err
	}
	if delimiter == '{' && end != json.Delim('}') || delimiter == '[' && end != json.Delim(']') {
		return errors.New("invalid corporate-action JSON closure")
	}
	return nil
}

func validCorporateActionPageToken(token string) bool {
	return len(token) <= 4096 && utf8.ValidString(token) && !strings.ContainsFunc(token, unicode.IsControl)
}

func corporateActions(r Recording) (*CorporateActionSnapshot, error) {
	decoder := json.NewDecoder(strings.NewReader(r.Original))
	decoder.UseNumber()
	if err := corporateActionJSONValue(decoder, 0); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("expected one corporate-action response")
	}

	var envelope struct {
		Actions json.RawMessage `json:"corporate_actions"`
		Next    json.RawMessage `json:"next_page_token"`
	}
	if json.Unmarshal([]byte(r.Original), &envelope) != nil || len(envelope.Actions) == 0 || string(envelope.Actions) == "null" || len(envelope.Next) == 0 {
		return nil, errors.New("invalid corporate-action REST envelope")
	}
	var groups map[string]json.RawMessage
	if json.Unmarshal(envelope.Actions, &groups) != nil || groups == nil {
		return nil, errors.New("invalid corporate-action groups")
	}
	out := &CorporateActionSnapshot{PageToken: r.PageToken, CaptureID: r.CaptureID, Revision: r.Revision, EvidenceClass: r.EvidenceClass, AccessRef: r.AccessRef, ReceivedAt: r.ReceivedAt.UTC(), Contract: CorporateActionContract, QueryDateBasis: "inclusive_process_date", DataQuality: "all", Region: "us", Symbols: slices.Clone(r.Symbols), Start: r.Start, End: r.End, AvailableAt: r.ReceivedAt.UTC(), RawRef: r.RawRef, SourceSHA256: r.SHA256, Records: []CorporateActionRecord{}, PageState: "terminal_page_only"}
	slices.Sort(out.Symbols)
	if string(envelope.Next) != "null" {
		var token string
		if json.Unmarshal(envelope.Next, &token) != nil || !validCorporateActionPageToken(token) {
			return nil, errors.New("invalid corporate-action continuation token")
		}
		out.NextPageToken = &token
		if token != "" {
			out.PageState = "more_pages_unavailable"
		}
	}
	kinds := make([]string, 0, len(groups))
	for kind := range groups {
		if len(kind) == 0 || len(kind) > 64 || strings.ContainsFunc(kind, func(c rune) bool { return (c < 'a' || c > 'z') && c != '_' }) {
			return nil, errors.New("invalid corporate-action group name")
		}
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	seen := map[string]bool{}
	for _, kind := range kinds {
		var records []json.RawMessage
		if json.Unmarshal(groups[kind], &records) != nil || records == nil {
			return nil, errors.New("corporate-action group must be an array")
		}
		for _, raw := range records {
			var identity struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &identity) != nil || len(identity.ID) == 0 || len(identity.ID) > 128 || strings.ContainsFunc(identity.ID, unicode.IsControl) || seen[identity.ID] {
				return nil, errors.New("invalid or duplicate corporate-action source identity")
			}
			seen[identity.ID] = true
			out.Records = append(out.Records, CorporateActionRecord{Kind: kind, SourceID: identity.ID, OriginalJSON: string(raw), SHA256: Digest(string(raw))})
			if len(out.Records) > 1000 {
				return nil, errors.New("corporate-action page exceeds declared limit")
			}
		}
	}
	return out, nil
}

// CorporateActionSnapshots returns every retained snapshot available by the
// explicit cut, in journal receipt order. Later captures never rewrite earlier
// snapshots or imply deletions from an earlier page. Collection completion is
// required for network captures; interrupted acquisitions remain raw evidence.
func (j *Journal) CorporateActionSnapshots(asOf time.Time) ([]CorporateActionSnapshot, error) {
	if !validTime(asOf) {
		return nil, errors.New("corporate-action inspection requires a valid as-of cut")
	}
	captures, err := j.captures()
	if err != nil {
		return nil, err
	}
	out := []CorporateActionSnapshot{}
	for _, c := range captures {
		if c.Result.CorporateActions == nil || c.Recording.ReceivedAt.After(asOf) {
			continue
		}
		// A retained capture can precede the terminal acquisition receipt after
		// a crash. Never promote that artifact into a completed collection.
		snapshot := *c.Result.CorporateActions
		if strings.HasPrefix(c.OriginalRef, "journal:"+j.ID+":collection-") {
			status, err := j.Collection(c.Recording.CaptureID)
			if err != nil {
				return nil, err
			}
			if status.State != "retained" {
				continue
			}
			var intent collectionIntent
			if _, err := j.readCollection(collectionName(c.Recording.CaptureID, "intent"), &intent); err != nil {
				return nil, err
			}
			requestedAt := intent.At.UTC()
			snapshot.RequestedAt = &requestedAt
			var done collectionCompletion
			if _, err := j.readCollection(collectionName(c.Recording.CaptureID, "complete"), &done); err != nil {
				return nil, err
			}
			if done.At.After(snapshot.AvailableAt) {
				snapshot.AvailableAt = done.At
			}
			if snapshot.AvailableAt.After(asOf) {
				continue
			}
		}
		out = append(out, snapshot)
	}
	return out, nil
}
