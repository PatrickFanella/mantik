package candidateevidence

import (
	"bytes"
	"testing"
)

func TestTransportRejectsAmbiguousAndOversizedJSON(t *testing.T) {
	for _, b := range [][]byte{[]byte(`null`), []byte(`{}`), []byte(`{"receiver_id":"x","receiver_id":"y"}`), []byte(`{"Receiver_id":"x"}`), []byte(`{} {}`), bytes.Repeat([]byte(" "), MaxReportBytes+1)} {
		if _, err := ReadReport(bytes.NewReader(b)); err == nil {
			t.Fatal("invalid report accepted")
		}
	}
	for _, b := range [][]byte{[]byte(`null`), []byte(`{}`), []byte(`{"schema":"x","schema":"y"}`), []byte(`{"Schema":"x"}`), []byte(`{} {}`), bytes.Repeat([]byte(" "), MaxArtifactBytes+1)} {
		if _, err := ReadArtifact(bytes.NewReader(b)); err == nil {
			t.Fatal("invalid artifact accepted")
		}
	}
}
