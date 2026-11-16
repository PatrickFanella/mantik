package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestStrategyGateAndRiskHelpAndExplicitDatabaseBoundary(t *testing.T) {
	for _, command := range []string{"strategy-gate", "capped-risk"} {
		t.Run(command, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if e := Run(context.Background(), []string{command, "--help"}, &out, &errOut); e != nil || !strings.Contains(errOut.String(), "database-env") {
				t.Fatal(e, errOut.String())
			}
			t.Setenv("MANTIK_EMPTY_GATE_DB", "")
			if e := Run(context.Background(), []string{command, "--database-env", "MANTIK_EMPTY_GATE_DB"}, &out, &errOut); e == nil {
				t.Fatal("implicit database accepted")
			}
			if e := Run(context.Background(), []string{command, "--not-a-flag"}, &out, &errOut); e == nil {
				t.Fatal("unknown flag accepted")
			}
		})
	}
}
