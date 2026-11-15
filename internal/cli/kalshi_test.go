package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestKalshiCollectionCLIRequiresDatabase(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runKalshiCollect(context.Background(), nil, &out, &errOut); err == nil {
		t.Fatal("missing database admitted")
	}
	if err := runKalshiCollect(context.Background(), []string{"--help"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
}
