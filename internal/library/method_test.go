package library

import "testing"

func TestNativeMethodPinsPreservePublishedBytes(t *testing.T) {
	for id, want := range map[string]string{"demo-threshold-v1": "630da964c81332cc73de97f0f73606171f220c0315b0204ffa0cae4e7739a8fb", "demo-half-size-v1": "0c41ab474b84927847dc2f6a7a556b0977465422847e4a8be61ace9915a432d4"} {
		ref, m, err := PinMethod(id)
		if err != nil || ref.SHA256 != want || ref.ID != id || ref.Schema != NativeMethodSchema || m.Strategy.ID != id {
			t.Fatal(ref, err)
		}
		m.Entry.Evaluation.Required[0].Actions[0] = "tampered"
		again, _, err := PinMethod(id)
		if err != nil || ref != again {
			t.Fatal("method alias changed pin", err)
		}
	}
	if _, _, err := PinMethod("invented"); err == nil {
		t.Fatal("unknown method accepted")
	}
}
