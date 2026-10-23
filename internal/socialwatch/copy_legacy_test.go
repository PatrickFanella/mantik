package socialwatch

import (
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
	"testing"
	"time"
)

func TestCopyLegacyWireCompatibility(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC)
	j, _ := New(testOwner)
	p := testPolicy("watch-a", "account-a")
	j, pr, e := Append(j, Command{Policy: &p}, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	c := testCapture("capture", "watch-a", pr.Entry.Policy.SHA256, testOriginal("post-1", "post", 1, at))
	j, _, e = Append(j, Command{Capture: &c}, func() time.Time { return at })
	if e != nil {
		t.Fatal(e)
	}
	if got := workerplan.Hash(j); got != "33c011e6d8b494947174ff9b5841792d18d3a5d14057e4eb86d85fa5f0175086" {
		t.Fatal("legacy journal bytes changed", got)
	}
}
