package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestResearchDeskFlagsFailBeforeStorageOrModel(t *testing.T) {
	t.Setenv("MANTIK_DESK_FLAG_TEST_URL", "postgres://invalid.invalid/mantik_test_flags")
	for _, args := range [][]string{
		{"--help"},
		{"--action", "activate", "--id", "desk", "--database-env", "MANTIK_DESK_FLAG_TEST_URL"},
		{"--action", "run", "--id", "desk", "--database-env", "MANTIK_DESK_FLAG_TEST_URL", "--input", "missing-private-file"},
		{"--action", "show"},
	} {
		var out, errOut bytes.Buffer
		e := Run(context.Background(), append([]string{"research-desk"}, args...), &out, &errOut)
		if args[0] == "--help" {
			if e != nil {
				t.Fatal(e)
			}
			continue
		}
		if e == nil {
			t.Fatal("invalid desk action admitted", args)
		}
	}
}
