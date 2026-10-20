package candidatelineage

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/dataset"
)

func TestSubmissionAndRegistryPins(t *testing.T) {
	c, p, at := fixture(t)
	// Package fixture is tied to compact JSON; changing transport must not alter it.
	d, err := dataset.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(d)
	s := Submission{Command: c, Dataset: data}
	raw, _ := json.Marshal(s)
	read, err := ReadSubmission(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(s, read) {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		append(bytes.Clone(raw), []byte("{}")...),
		[]byte(strings.Replace(string(raw), `"command":`, `"Command":`, 1)),
		[]byte(strings.Replace(string(raw), `"command":`, `"command":null,"command":`, 1)),
		[]byte(strings.Replace(string(raw), `"dataset":`, `"extra":true,"dataset":`, 1)),
	} {
		if _, err := ReadSubmission(bytes.NewReader(bad)); err == nil {
			t.Fatal("ambiguous transport accepted")
		}
	}
	s.Dataset = append(bytes.Clone(data), '\n')
	raw, _ = json.Marshal(s)
	if _, err := ReadSubmission(bytes.NewReader(raw)); err == nil {
		t.Fatal("changed raw bytes accepted")
	}
	r, err := Append(nil, c, p, at)
	if err != nil {
		t.Fatal(err)
	}
	h, err := RegistryFor(c.ReceiverID, []Registration{r})
	if err != nil || h.Validate() != nil {
		t.Fatal(err)
	}
	empty, err := RegistryFor(c.ReceiverID, nil)
	if err != nil || empty.Registrations == nil || empty.Validate() != nil {
		t.Fatal(err)
	}
	h.Registrations[0].Candidate.Hypothesis.Declaration.Methods[0].ID = "changed"
	if r.Validate() != nil {
		t.Fatal("registry aliases caller records")
	}
	for _, kind := range []string{"null", "receiver", "authority", "qualified", "execution", "hash"} {
		h, _ := RegistryFor(c.ReceiverID, []Registration{r})
		switch kind {
		case "null":
			h.Registrations = nil
		case "receiver":
			h.ReceiverID = "receiver-other_other_other"
		case "authority":
			h.Authority = "qualified"
		case "qualified":
			h.QualificationCredit = true
		case "execution":
			h.ExecutionStarted = true
		case "hash":
			h.SHA256 = "wrong"
		}
		if kind != "hash" {
			h.SHA256 = registryHash(h)
		}
		if h.Validate() == nil {
			t.Fatal("forged registry accepted", kind)
		}
	}
	a := Artifact{Schema: ArtifactSchema, Registration: r, Dataset: data}
	if a.Validate() != nil {
		t.Fatal("valid artifact rejected")
	}
	a.Dataset = append(bytes.Clone(data), '\n')
	if a.Validate() == nil {
		t.Fatal("artifact lost raw pin")
	}
}
