package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestForwardAssessmentCLI(t *testing.T) {
	var out, errOut bytes.Buffer
	if e := Run(context.Background(), []string{"forward-assessment", "--help"}, &out, &errOut); e != nil || errOut.Len() == 0 {
		t.Fatal("help", e)
	}
	for _, args := range [][]string{{"forward-assessment"}, {"forward-assessment", "--passed=true"}, {"forward-assessment", "--action=promote"}, {"forward-assessment", "--action=assess", "--input=imported-result.json"}} {
		if e := Run(context.Background(), args, &out, &errOut); e == nil {
			t.Fatal("accepted unbounded CLI input", args)
		}
	}
}
