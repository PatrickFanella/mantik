// Package replay owns immutable research datasets and point-in-time fixture runs.
// It does not contact providers or ingest into the running service.
package replay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
	"git.subcult.tv/subculture-collective/mantik/internal/workflow"
)

const DatasetSchema = "mantik-dataset-v1"
const maxDatasetBytes = 8 << 20

var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)

type Mapping struct {
	SourceSymbol string `json:"source_symbol"`
	Symbol       string `json:"symbol"`
	Version      string `json:"version"`
}

type Access struct {
	SourceRef   string `json:"source_ref"`
	License     string `json:"license"`
	RetrievedBy string `json:"retrieved_by"`
}

type Record struct {
	Key         string    `json:"key"`
	Revision    int       `json:"revision"`
	OccurredAt  time.Time `json:"occurred_at"`
	AvailableAt time.Time `json:"available_at"`
	ReceivedAt  time.Time `json:"received_at"`
	// Original is a string so serialization preserves the exact source bytes.
	Original   string  `json:"original"`
	SHA256     string  `json:"sha256"`
	Instrument Mapping `json:"instrument"`
	Access     Access  `json:"access"`
	Quality    string  `json:"quality"`
	Reason     string  `json:"reason,omitempty"`
}

type Dataset struct {
	Schema        string   `json:"schema"`
	ID            string   `json:"id"`
	Version       string   `json:"version"`
	EvidenceClass string   `json:"evidence_class"`
	Records       []Record `json:"records"`
	SHA256        string   `json:"sha256"`
}

func Digest(raw string) string {
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

// Seal validates and copies the dataset before assigning its content identity.
func Seal(d Dataset) (Dataset, error) {
	if err := d.validateRecords(); err != nil {
		return Dataset{}, err
	}
	d.SHA256 = ""
	raw, err := json.Marshal(d)
	if err != nil {
		return Dataset{}, err
	}
	if len(raw) > maxDatasetBytes {
		return Dataset{}, errors.New("dataset exceeds 8 MiB")
	}
	var sealed Dataset
	if err := json.Unmarshal(raw, &sealed); err != nil {
		return Dataset{}, err
	}
	sealed.SHA256 = Digest(string(raw))
	return sealed, nil
}

func (d Dataset) Validate() error {
	sealed, err := Seal(d)
	if err != nil {
		return err
	}
	if d.SHA256 == "" || sealed.SHA256 != d.SHA256 {
		return errors.New("dataset content identity mismatch")
	}
	return nil
}

func Read(input io.Reader) (Dataset, error) {
	d, err := Decode(input)
	if err != nil {
		return Dataset{}, err
	}
	return d, d.Validate()
}

// Decode only checks JSON shape and size. Execution must still call Validate;
// experiment admission uses this to retain failures of a declared dataset.
func Decode(input io.Reader) (Dataset, error) {
	raw, err := io.ReadAll(io.LimitReader(input, maxDatasetBytes+1))
	if err != nil {
		return Dataset{}, err
	}
	if len(raw) > maxDatasetBytes {
		return Dataset{}, errors.New("dataset exceeds 8 MiB")
	}
	var d Dataset
	if err := decode(string(raw), &d); err != nil {
		return Dataset{}, err
	}
	return d, nil
}

func decode(raw string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func (d Dataset) validateRecords() error {
	if d.Schema != DatasetSchema || !keyPattern.MatchString(d.ID) || !keyPattern.MatchString(d.Version) || d.EvidenceClass != "synthetic_unqualified" || len(d.Records) == 0 || len(d.Records) > 10000 {
		return errors.New("unsupported or incomplete fixture dataset")
	}
	seen := map[string]string{}
	for _, r := range d.Records {
		if !keyPattern.MatchString(r.Key) || r.Revision < 1 || r.Revision > 1000000 || r.OccurredAt.IsZero() || r.AvailableAt.IsZero() || r.ReceivedAt.IsZero() || r.OccurredAt.After(r.AvailableAt) || r.OccurredAt.After(r.ReceivedAt) {
			return fmt.Errorf("invalid identity or times for %q", r.Key)
		}
		if r.Instrument.SourceSymbol == "" || r.Instrument.Symbol != "DEMO" || r.Instrument.Version != "fixture-map-v1" || r.Access.SourceRef == "" || r.Access.License == "" || r.Access.RetrievedBy == "" {
			return fmt.Errorf("missing mapping or access provenance for %q", r.Key)
		}
		if r.Quality != "usable" && r.Quality != "quarantined" && r.Quality != "missing" {
			return fmt.Errorf("unknown quality for %q", r.Key)
		}
		if r.Quality != "usable" && r.Reason == "" {
			return fmt.Errorf("quality reason required for %q", r.Key)
		}
		if r.SHA256 != Digest(r.Original) {
			return fmt.Errorf("source content identity mismatch for %q", r.Key)
		}
		if r.Quality == "usable" {
			if _, err := r.quote(); err != nil {
				return err
			}
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		id := fmt.Sprintf("%s-r%d", r.Key, r.Revision)
		if previous, exists := seen[id]; exists && previous != string(raw) {
			return fmt.Errorf("conflicting revision %q", id)
		}
		seen[id] = string(raw)
	}
	return nil
}

func (r Record) quote() (domain.Observation, error) {
	var o domain.Observation
	if err := decode(r.Original, &o); err != nil {
		return o, fmt.Errorf("invalid original quote %q: %w", r.Key, err)
	}
	if o.ID != r.Key || o.Symbol != r.Instrument.SourceSymbol || !o.OccurredAt.Equal(r.OccurredAt) || !o.ReceivedAt.Equal(r.ReceivedAt) {
		return o, fmt.Errorf("source metadata mismatch for %q", r.Key)
	}
	o.Symbol = r.Instrument.Symbol
	if err := workflow.Validate(o); err != nil {
		return o, err
	}
	return o, nil
}

func (r Record) visibleAt() time.Time {
	if r.ReceivedAt.After(r.AvailableAt) {
		return r.ReceivedAt
	}
	return r.AvailableAt
}

func (r Record) VisibleAt() time.Time { return r.visibleAt() }

// ExecutionObservation derives an isolated input; original bytes remain intact.
func (r Record) ExecutionObservation() (domain.Observation, error) {
	if r.Quality != "usable" || r.Revision != 1 {
		return domain.Observation{}, errors.New("record requires data-quality review")
	}
	o, err := r.quote()
	if err != nil {
		return o, err
	}
	o.ID = fmt.Sprintf("%s-r%d", r.Key, r.Revision)
	o.ReceivedAt = r.visibleAt().UTC()
	return o, nil
}
