package candidatelineage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/candidate"
	"git.subcult.tv/subculture-collective/mantik/internal/hypothesis"
	"git.subcult.tv/subculture-collective/mantik/internal/replay"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

func fixture(t *testing.T) (Command, hypothesis.Publication, time.Time) {
	t.Helper()
	d, err := replay.FixtureDataset()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	// Invalid Go must remain available for later retained compile failures.
	p, err := candidate.New([]byte("not valid Go\n"), hypothesis.Fixture(), raw, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 5, 17, 0, 0, 0, time.UTC)
	pub := hypothesis.Publication{Schema: hypothesis.PublicationSchema, ID: "publication", ReceiverID: "receiver-abcdefghijklmnop", PublishedAt: at, OriginBindingVerified: true, Sealed: p.Hypothesis}
	return Command{ReceiverID: pub.ReceiverID, RequestID: "first", PublicationID: pub.ID, Candidate: p}, pub, at
}

func TestAppendImmutableBranchesAndRetries(t *testing.T) {
	c, p, at := fixture(t)
	first, err := Append(nil, c, p, at)
	if err != nil || first.Validate() != nil || first.PublicationSHA256 != workerplan.Hash(p) || first.Candidate.SHA256 != c.Candidate.SHA256 || first.DatasetAvailabilityVerified || first.ExecutionStarted || first.QualificationCredit {
		t.Fatal(first, err)
	}
	history := []Registration{first}
	// Retry returns the original declaration, including time and ordinal.
	again, err := Append(history, c, p, time.Time{})
	if err != nil || !reflect.DeepEqual(again, first) {
		t.Fatal("retry changed", err)
	}
	c.RequestID, c.ParentID = "child", first.ID
	child, err := Append(history, c, p, at.Add(time.Second))
	if err != nil || child.ParentSHA256 != first.SHA256 || child.Sequence != 2 || child.Candidate.ID != first.Candidate.ID {
		t.Fatal(child, err)
	}
	history = append(history, child)
	if child.VerifyBinding(p, &first) != nil || child.VerifyBinding(p, nil) == nil || first.VerifyBinding(p, &child) == nil {
		t.Fatal("binding recheck lost parent requirements")
	}
	changedPublication := p
	changedPublication.ReviewID = "changed"
	if first.VerifyBinding(changedPublication, nil) == nil {
		t.Fatal("binding recheck accepted another publication pin")
	}
	c.RequestID = "sibling"
	sibling, err := Append(history, c, p, at.Add(2*time.Second))
	if err != nil || sibling.ParentSHA256 != first.SHA256 || sibling.SHA256 == child.SHA256 {
		t.Fatal(sibling, err)
	}
	history = append(history, sibling)
	if ValidateHistory(c.ReceiverID, history) != nil {
		t.Fatal("valid forest rejected")
	}
	// Parent may name another publication of the identical hypothesis.
	c.RequestID, c.ParentID, c.PublicationID = "republished", child.ID, "publication2"
	p.ID, p.PublishedAt = c.PublicationID, at.Add(3*time.Second)
	r, err := Append(history, c, p, p.PublishedAt)
	if err != nil || r.PublicationSHA256 == child.PublicationSHA256 {
		t.Fatal(r, err)
	}
	// Every returned nested value is detached from caller-owned input/history.
	old := first.Candidate.Hypothesis.Declaration.Methods[0].ID
	c.Candidate.Hypothesis.Declaration.Methods[0].ID = "changed"
	again.Candidate.Hypothesis.Declaration.Methods[0].ID = "also-changed"
	if first.Candidate.Hypothesis.Declaration.Methods[0].ID != old || history[0].Validate() != nil || r.Validate() != nil {
		t.Fatal("nested alias changed retained pins")
	}
}

func TestPublicationAndParentFences(t *testing.T) {
	for _, name := range []string{"receiver", "bad-receiver", "publication-id", "publication-schema", "unbound-publication", "publication-hash", "different-declaration", "early-clock", "zero-clock", "zero-publication-clock", "future-year", "missing-parent", "unexpected-parent", "self-parent", "parent-receiver", "parent-hypothesis", "parent-time", "parent-sequence", "parent-corrupt"} {
		t.Run(name, func(t *testing.T) {
			c, p, at := fixture(t)
			base, err := Append(nil, c, p, at)
			if err != nil {
				t.Fatal(err)
			}
			c.RequestID = "next"
			var parent *Registration
			switch name {
			case "receiver":
				p.ReceiverID = "receiver-otherotherother1"
			case "bad-receiver":
				c.ReceiverID = "bad"
			case "publication-id":
				p.ID = "other"
			case "publication-schema":
				p.Schema = "other"
			case "unbound-publication":
				p.OriginBindingVerified = false
			case "publication-hash":
				p.Sealed.DeclarationSHA256 = strings.Repeat("b", 64)
			case "different-declaration":
				h := detached(p.Sealed.Declaration)
				h.Label = "another"
				p.Sealed, _ = hypothesis.Seal(h)
			case "early-clock":
				at = at.Add(-time.Second)
			case "zero-clock":
				at = time.Time{}
			case "zero-publication-clock":
				p.PublishedAt = time.Time{}
			case "future-year":
				at = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			case "missing-parent":
				c.ParentID = base.ID
			case "unexpected-parent":
				parent = &base
			case "self-parent":
				c.ParentID = c.RequestID
			default:
				c.ParentID, parent = base.ID, &base
				switch name {
				case "parent-receiver":
					base.ReceiverID = "receiver-otherotherother1"
				case "parent-hypothesis":
					h := detached(base.Candidate.Hypothesis.Declaration)
					h.Label = "other"
					d, _ := replay.FixtureDataset()
					raw, _ := json.Marshal(d)
					base.Candidate, _ = candidate.New([]byte("bad go"), h, raw, base.Candidate.Toolchain.ImageSHA256)
				case "parent-time":
					base.RegisteredAt = at.Add(time.Second)
				case "parent-sequence":
					base.Sequence = 2
				case "parent-corrupt":
					base.Schema = "bad"
				}
				base.SHA256 = seal(base)
			}
			if _, err := Register(c, p, parent, 2, at); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestHistoryFailsClosedAndRetriesConflict(t *testing.T) {
	c, p, at := fixture(t)
	first, _ := Append(nil, c, p, at)
	c.RequestID, c.ParentID = "child", first.ID
	child, _ := Append([]Registration{first}, c, p, at.Add(time.Second))
	for name, mutate := range map[string]func([]Registration) []Registration{
		"gap": func(h []Registration) []Registration { h[1].Sequence = 3; return h },
		"duplicate": func(h []Registration) []Registration {
			h[1].ID, h[1].ParentID, h[1].ParentSHA256 = h[0].ID, "", ""
			return h
		},
		"missing-parent": func(h []Registration) []Registration { h[1].ParentID = "missing"; return h },
		"parent-pin":     func(h []Registration) []Registration { h[1].ParentSHA256 = strings.Repeat("b", 64); return h },
		"forward-parent": func(h []Registration) []Registration {
			h[0].ParentID, h[0].ParentSHA256 = h[1].ID, h[1].SHA256
			return h
		},
		"time-rollback":         func(h []Registration) []Registration { h[1].RegisteredAt = at.Add(-time.Second); return h },
		"receiver":              func(h []Registration) []Registration { h[1].ReceiverID = "receiver-otherotherother1"; return h },
		"authority":             func(h []Registration) []Registration { h[1].Authority = "admitted"; return h },
		"qualified":             func(h []Registration) []Registration { h[1].QualificationCredit = true; return h },
		"executed":              func(h []Registration) []Registration { h[1].ExecutionStarted = true; return h },
		"data-verified":         func(h []Registration) []Registration { h[1].DatasetAvailabilityVerified = true; return h },
		"parent-sha-without-id": func(h []Registration) []Registration { h[0].ParentSHA256 = h[1].SHA256; return h },
		"different-hypothesis": func(h []Registration) []Registration {
			other, _, _ := fixture(t)
			decl := other.Candidate.Hypothesis.Declaration
			decl.Label = "different"
			d, _ := replay.FixtureDataset()
			raw, _ := json.Marshal(d)
			h[1].Candidate, _ = candidate.New([]byte("bad go"), decl, raw, other.Candidate.Toolchain.ImageSHA256)
			return h
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := mutate(detached([]Registration{first, child}))
			for i := range h {
				h[i].SHA256 = seal(h[i])
			}
			if ValidateHistory(c.ReceiverID, h) == nil {
				t.Fatal("forged history accepted")
			}
			// Corruption of any row blocks even an exact retry of an unaffected row.
			c, p, _ := fixture(t)
			if _, err := Append(h, c, p, at); err == nil {
				t.Fatal("retry hid corrupt history")
			}
		})
	}
	for _, change := range []string{"candidate", "publication", "parent"} {
		t.Run("retry-"+change, func(t *testing.T) {
			c, p, _ := fixture(t)
			switch change {
			case "candidate":
				d, _ := replay.FixtureDataset()
				raw, _ := json.Marshal(d)
				c.Candidate, _ = candidate.New([]byte("different invalid Go"), c.Candidate.Hypothesis.Declaration, raw, c.Candidate.Toolchain.ImageSHA256)
			case "publication":
				p.ReviewID = "different-origin"
			case "parent":
				c.ParentID = child.ID
			}
			if _, err := Append([]Registration{first, child}, c, p, at); !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestCapacityClockAndStrictRead(t *testing.T) {
	c, p, at := fixture(t)
	var history []Registration
	for i := range MaxRegistrations {
		c.RequestID = fmt.Sprintf("request-%02d", i)
		r, err := Append(history, c, p, at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		history = append(history, r)
	}
	if ValidateHistory(c.ReceiverID, history) != nil {
		t.Fatal("capacity history rejected")
	}
	if r, err := Append(history, c, p, time.Time{}); err != nil || r.SHA256 != history[len(history)-1].SHA256 {
		t.Fatal("capacity retry refused", err)
	}
	c.RequestID = "excess"
	if _, err := Append(history, c, p, at.Add(time.Hour)); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if _, err := Append(history[:1], c, p, at.Add(-time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if ValidateHistory(c.ReceiverID, append(history, history[0])) == nil {
		t.Fatal("overcapacity accepted")
	}
	raw, _ := json.Marshal(c)
	read, err := Read(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(read, c) {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{
		append(append([]byte(nil), raw...), []byte(" {}")...),
		[]byte(strings.Replace(string(raw), `"receiver_id":`, `"Receiver_id":`, 1)),
		[]byte(strings.Replace(string(raw), `"receiver_id":`, `"receiver_id":"duplicate","receiver_id":`, 1)),
		[]byte(strings.Replace(string(raw), `"receiver_id":`, `"execution_started":true,"receiver_id":`, 1)),
		bytes.Repeat([]byte(" "), MaxBytes+1),
	} {
		if _, err := Read(bytes.NewReader(bad)); err == nil {
			t.Fatal("ambiguous/oversized command accepted")
		}
	}
}
