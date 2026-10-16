package workertrial_test

import (
	"encoding/json"
	"strings"
	"testing"

	"git.subcult.tv/subculture-collective/mantik/internal/workertrial"
)

func TestReportStrictShapeAndFullCaptureBound(t *testing.T) {
	c := workertrial.ReportCommand{ReceiverID: "receiver-abcdefghijklmnop", AttemptID: "physical", IntentSHA256: strings.Repeat("a", 64), ReportedStatus: "incomplete", Stdout: strings.Repeat("x", (8<<20)+1)}
	raw, _ := json.Marshal(c)
	if got, err := workertrial.ReadReport(strings.NewReader(string(raw))); err != nil || got.Stdout != c.Stdout {
		t.Fatal("bounded capture over dataset limit rejected", err)
	}
	for _, change := range []func(*workertrial.ReportCommand){func(c *workertrial.ReportCommand) { c.Stdout = "\x00" }, func(c *workertrial.ReportCommand) { c.Stderr = string([]byte{255}) }, func(c *workertrial.ReportCommand) { c.Stdout = strings.Repeat("x", workertrial.MaxStdout+1) }, func(c *workertrial.ReportCommand) { c.Stderr = strings.Repeat("x", workertrial.MaxStderr+1) }, func(c *workertrial.ReportCommand) { c.ReportedStatus = "running" }} {
		v := c
		change(&v)
		if v.Validate() == nil {
			t.Fatal("invalid report accepted")
		}
	}
	small := c
	small.Stdout = ""
	raw, _ = json.Marshal(small)
	for _, body := range []string{string(raw) + "{}", strings.Replace(string(raw), `"stdout":`, `"stdout":"x","stdout":`, 1), strings.Replace(string(raw), `"stdout":`, `"Stdout":`, 1), strings.Replace(string(raw), `"stdout":`, `"qualification_credit":true,"stdout":`, 1)} {
		if _, err := workertrial.ReadReport(strings.NewReader(body)); err == nil {
			t.Fatal("ambiguous report accepted")
		}
	}
}
