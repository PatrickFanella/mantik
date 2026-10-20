package candidate

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	replay "git.subcult.tv/subculture-collective/mantik/internal/dataset"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (Package, []byte) {
	t.Helper()
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	p, err := New([]byte("not valid Go; retain failed candidates too\n"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	return p, raw
}

func TestCandidateIdentityAndExactArtifacts(t *testing.T) {
	p, raw := fixture(t)
	if p.Validate() != nil || p.VerifyDataset(raw) != nil {
		t.Fatal("valid artifacts rejected")
	}
	again, err := New([]byte(p.Source.Text), p.Hypothesis.Declaration, raw, p.Toolchain.ImageSHA256)
	if err != nil || !reflect.DeepEqual(p, again) {
		t.Fatal("identical declaration changed")
	}
	for _, change := range []string{"source", "hypothesis", "dataset bytes", "toolchain"} {
		t.Run(change, func(t *testing.T) {
			s, h, d, img := []byte(p.Source.Text), p.Hypothesis.Declaration, raw, p.Toolchain.ImageSHA256
			switch change {
			case "source":
				s = append(s, ' ')
			case "hypothesis":
				h.Label = "different-origin-label"
			case "dataset bytes":
				d = append(append([]byte(nil), raw...), '\n')
			case "toolchain":
				img = "sha256:" + strings.Repeat("b", 64)
			}
			changed, err := New(s, h, d, img)
			if err != nil || changed.ID == p.ID {
				t.Fatal("changed provenance did not change identity", err)
			}
		})
	}
	if p.VerifyDataset(append(raw, '\n')) == nil {
		t.Fatal("raw artifact change accepted")
	}
	encoded, _ := json.Marshal(p)
	read, err := Read(bytes.NewReader(encoded))
	if err != nil || !reflect.DeepEqual(read, p) {
		t.Fatal("readback changed", err)
	}
	// Mutating the caller's declaration must not rewrite a previously sealed pin.
	h := hypothesis.Fixture()
	sealed, err := New([]byte(p.Source.Text), h, raw, p.Toolchain.ImageSHA256)
	if err != nil {
		t.Fatal(err)
	}
	h.Methods[0].ID = "mutated"
	if sealed.Validate() != nil {
		t.Fatal("caller retained ownership of declaration slices")
	}
}

func TestCandidateRejectsForgedAuthorityAndBindings(t *testing.T) {
	p, _ := fixture(t)
	for name, mutate := range map[string]func(*Package){
		"source bytes":            func(p *Package) { p.Source.Text += "changed" },
		"path":                    func(p *Package) { p.Source.Reference = "../../main.go" },
		"language":                func(p *Package) { p.Source.Language = "python" },
		"source length":           func(p *Package) { p.Source.Bytes++ },
		"hypothesis":              func(p *Package) { p.Hypothesis.Declaration.HorizonSeconds++ },
		"source origins verified": func(p *Package) { p.Hypothesis.OriginsVerified = true },
		"dataset path":            func(p *Package) { p.Dataset.Reference = "/etc/passwd" },
		"dataset identity":        func(p *Package) { p.Dataset.CanonicalSHA256 = "bad" },
		"runtime":                 func(p *Package) { p.Toolchain.Runtime = "go1.26.8" },
		"mutable image":           func(p *Package) { p.Toolchain.ImageSHA256 = "golang:latest" },
		"qualified":               func(p *Package) { p.QualificationCredit = true },
		"compiled":                func(p *Package) { p.SourceCompiled = true },
		"executed":                func(p *Package) { p.SourceExecuted = true },
		"image verified":          func(p *Package) { p.ImageIdentityVerified = true },
		"service bound":           func(p *Package) { p.ServiceBindingVerified = true },
		"authority":               func(p *Package) { p.Authority = "execution" },
	} {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(p)
			var changed Package
			_ = json.Unmarshal(raw, &changed)
			mutate(&changed)
			// Even a caller who recomputes the content hash cannot grant authority.
			changed.SHA256 = identity(changed)
			changed.ID = "candidate-" + changed.SHA256
			if changed.Validate() == nil {
				t.Fatal("invalid declaration accepted")
			}
		})
	}
}

func TestCandidateStrictReadAndInputBounds(t *testing.T) {
	p, d := fixture(t)
	raw, _ := json.Marshal(p)
	for _, bad := range [][]byte{
		append(append([]byte(nil), raw...), []byte(" {}")...),
		[]byte(strings.Replace(string(raw), `"schema":`, `"Schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"extra":true,"schema":`, 1)),
		[]byte(strings.Replace(string(raw), `"schema":`, `"schema":"duplicate","schema":`, 1)),
		bytes.Repeat([]byte(" "), MaxPackageBytes+1),
	} {
		if _, err := Read(bytes.NewReader(bad)); err == nil {
			t.Fatal("ambiguous/oversized JSON accepted")
		}
	}
	for _, src := range [][]byte{nil, {0xff}, {'a', 0}, bytes.Repeat([]byte("x"), MaxSourceBytes+1)} {
		if _, err := New(src, p.Hypothesis.Declaration, d, p.Toolchain.ImageSHA256); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	var dataset replay.Dataset
	_ = json.Unmarshal(d, &dataset)
	dataset.SHA256 = strings.Repeat("0", 64)
	bad, _ := json.Marshal(dataset)
	if _, err := New([]byte(p.Source.Text), p.Hypothesis.Declaration, bad, p.Toolchain.ImageSHA256); err == nil {
		t.Fatal("changed dataset seal accepted")
	}
	if workerplan.Digest([]byte(p.Source.Text)) != p.Source.SHA256 {
		t.Fatal("source bytes not retained")
	}
}
