package strategyidentity

import "testing"

func TestIdentityRetainsPreExtractionHash(t *testing.T) {
	s := Spec{Name: "incubation-fixture", Version: "1", Data: []string{"daily_bars_adjusted"}}
	const original = "strategy-31148cf29199f7c3ef444dc29ba12c4f5d91745c501b00a69b27dd2593d1b473"
	if got, e := s.ID(); e != nil || got != original {
		t.Fatal(got, e)
	}
}
