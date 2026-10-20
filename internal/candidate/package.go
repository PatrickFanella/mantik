// Package candidate seals caller-supplied research source without compiling,
// executing, publishing or admitting it.
package candidate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

const Schema = "mantik-candidate-source-v1"
const MaxSourceBytes = 64 << 10
const MaxPackageBytes = 512 << 10

var ErrInvalid = errors.New("invalid or unsupported candidate source declaration")
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var image = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type Source struct {
	Reference string `json:"reference"`
	Language  string `json:"language"`
	Text      string `json:"text"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
}

type Toolchain struct {
	Runtime     string `json:"runtime"`
	ImageSHA256 string `json:"image_sha256"`
}

// Package is an offline content-addressed declaration. Pins are assertions
// until the corresponding artifacts and actual runtime are independently checked.
type Package struct {
	Schema                 string            `json:"schema"`
	ID                     string            `json:"id"`
	Source                 Source            `json:"source"`
	Hypothesis             hypothesis.Result `json:"hypothesis"`
	Dataset                workerplan.Input  `json:"dataset"`
	Toolchain              Toolchain         `json:"toolchain"`
	Status                 string            `json:"status"`
	Authority              string            `json:"authority"`
	QualificationCredit    bool              `json:"qualification_credit"`
	SourceCompiled         bool              `json:"source_compiled"`
	SourceExecuted         bool              `json:"source_executed"`
	ImageIdentityVerified  bool              `json:"image_identity_verified"`
	ServiceBindingVerified bool              `json:"service_binding_verified"`
	SHA256                 string            `json:"sha256"`
}

func identity(p Package) string {
	p.ID, p.SHA256 = "", ""
	return workerplan.Hash(p)
}

// New retains exact UTF-8 source, including invalid Go. Failed compilation must
// remain a possible later worker outcome rather than disappearing at declaration.
func New(source []byte, h hypothesis.Declaration, rawDataset []byte, toolchainImage string) (Package, error) {
	if len(source) < 1 || len(source) > MaxSourceBytes || !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 || len(rawDataset) > workerplan.MaxInputBytes {
		return Package{}, ErrInvalid
	}
	sealed, err := hypothesis.Seal(h)
	if err != nil {
		return Package{}, ErrInvalid
	}
	var data replay.Dataset
	if workerplan.ReadDatasetShape(rawDataset, &data) != nil || data.Validate() != nil || sealed.Declaration.EvidenceClass != data.EvidenceClass || sealed.Declaration.Instrument != "DEMO" {
		return Package{}, ErrInvalid
	}
	p := Package{Schema: Schema, Source: Source{Reference: "candidate.go", Language: "go", Text: string(source), Bytes: len(source), SHA256: workerplan.Digest(source)}, Hypothesis: sealed,
		Dataset:   workerplan.Input{Reference: "dataset.json", MediaType: "application/json", Bytes: len(rawDataset), SHA256: workerplan.Digest(rawDataset), CanonicalSHA256: data.SHA256},
		Toolchain: Toolchain{Runtime: workerplan.Runtime, ImageSHA256: toolchainImage}, Status: "declared", Authority: "caller_supplied_research_source_only"}
	p.SHA256 = identity(p)
	p.ID = "candidate-" + p.SHA256
	if p.Validate() != nil {
		return Package{}, ErrInvalid
	}
	return p, nil
}

func (p Package) Validate() error {
	sealed, err := hypothesis.Seal(p.Hypothesis.Declaration)
	if err != nil || !reflect.DeepEqual(sealed, p.Hypothesis) || p.Hypothesis.Declaration.EvidenceClass != "synthetic_unqualified" || p.Hypothesis.Declaration.Instrument != "DEMO" {
		return ErrInvalid
	}
	s := p.Source
	if p.Schema != Schema || s.Reference != "candidate.go" || s.Language != "go" || s.Bytes < 1 || s.Bytes > MaxSourceBytes || s.Bytes != len(s.Text) || !utf8.ValidString(s.Text) || strings.ContainsRune(s.Text, 0) || s.SHA256 != workerplan.Digest([]byte(s.Text)) {
		return ErrInvalid
	}
	d := p.Dataset
	if d.Reference != "dataset.json" || d.MediaType != "application/json" || d.Bytes < 1 || d.Bytes > workerplan.MaxInputBytes || !digest.MatchString(d.SHA256) || !digest.MatchString(d.CanonicalSHA256) || p.Toolchain.Runtime != workerplan.Runtime || !image.MatchString(p.Toolchain.ImageSHA256) {
		return ErrInvalid
	}
	if p.Status != "declared" || p.Authority != "caller_supplied_research_source_only" || p.QualificationCredit || p.SourceCompiled || p.SourceExecuted || p.ImageIdentityVerified || p.ServiceBindingVerified || p.SHA256 != identity(p) || p.ID != "candidate-"+p.SHA256 {
		return ErrInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > MaxPackageBytes {
		return ErrInvalid
	}
	return nil
}

// VerifyDataset checks supplied bytes, typed content and canonical identity.
// A declaration read alone never attests the presence of the dataset artifact.
func (p Package) VerifyDataset(raw []byte) error {
	if p.Validate() != nil || len(raw) != p.Dataset.Bytes || workerplan.Digest(raw) != p.Dataset.SHA256 {
		return ErrInvalid
	}
	var data replay.Dataset
	if workerplan.ReadDatasetShape(raw, &data) != nil || data.Validate() != nil || data.SHA256 != p.Dataset.CanonicalSHA256 || data.EvidenceClass != p.Hypothesis.Declaration.EvidenceClass {
		return ErrInvalid
	}
	return nil
}

func Read(reader io.Reader) (Package, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MaxPackageBytes+1))
	var p Package
	if err != nil || workerplan.ReadBoundedShape(raw, &p, MaxPackageBytes) != nil || p.Validate() != nil {
		return Package{}, ErrInvalid
	}
	return p, nil
}
