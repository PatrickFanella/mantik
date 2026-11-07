package alpaca

import (
	"reflect"
	"testing"
)

// A remembered normalization is returned as a detached copy, so a caller that
// edits its result cannot change what a later replay of the capture sees.
func TestNormalizeMemoReturnsDetachedCopies(t *testing.T) {
	r := recording(goodQuotes)
	first, err := Normalize(r)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := normalize(r)
	first.Inputs[0].PriceCents++
	first.Inputs[0].Provenance.Quality = "edited"
	first.Coverage[0].Status = "edited"
	first.Unsupported[0] = "edited"
	again, err := Normalize(r)
	if err != nil || !reflect.DeepEqual(again, want) {
		t.Fatal("remembered normalization shared caller memory", again, err)
	}
	changed := r
	changed.ReceivedAt = changed.ReceivedAt.Add(1)
	other, err := Normalize(changed)
	if err != nil || other.Inputs[0].Provenance.AvailableAt.Equal(want.Inputs[0].Provenance.AvailableAt) {
		t.Fatal("memo ignored a changed capture field", err)
	}
}
