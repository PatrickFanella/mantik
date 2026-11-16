package forward

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBenchmarkOriginalBytesAndClocks(t *testing.T) {
	at := time.Date(2027, 1, 5, 15, 0, 0, 0, time.UTC)
	raw := []byte("{\n  \"StrategyID\":\"cash-source\",\"Index\":\"100\",\"EvidenceClass\":\"prospective_market_unqualified\",\"ObservedAt\":\"2027-01-05T15:00:00Z\",\"ReceivedAt\":\"2027-01-05T15:00:00Z\"\n}")
	b := BenchmarkObservation{StrategyID: "cash-source", Index: "100", EvidenceClass: "prospective_market_unqualified", ObservedAt: at, ReceivedAt: at, Raw: raw, SourceSHA256: SourceDigest(raw)}
	encoded, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	var decoded BenchmarkObservation
	if e = json.Unmarshal(encoded, &decoded); e != nil || string(decoded.Raw) != string(raw) || decoded.Validate(at) != nil {
		t.Fatal("source bytes changed", e)
	}
	for name, change := range map[string]func(*BenchmarkObservation){"missing": func(v *BenchmarkObservation) { v.Raw = nil }, "wrong-index": func(v *BenchmarkObservation) { v.Index = "101" }, "synthetic": func(v *BenchmarkObservation) { v.EvidenceClass = "synthetic_fixture" }, "future": func(v *BenchmarkObservation) { v.ReceivedAt = at.Add(time.Second) }, "wrong-benchmark": func(v *BenchmarkObservation) { v.StrategyID = "another-source" }, "unclassified": func(v *BenchmarkObservation) { v.EvidenceClass = "" }} {
		t.Run(name, func(t *testing.T) {
			v := b
			change(&v)
			if v.Validate(at) == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
}
