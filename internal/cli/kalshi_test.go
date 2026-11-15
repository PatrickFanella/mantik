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

func TestWeatherCLIValidation(t *testing.T) {
	for _, command := range []string{"kalshi-weather-predict", "kalshi-weather-settle", "kalshi-weather-calibration"} {
		t.Run(command, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if err := Run(context.Background(), []string{command, "--help"}, &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if err := Run(context.Background(), []string{command, "--database-url="}, &out, &errOut); err == nil {
				t.Fatal("missing database admitted")
			}
			if command != "kalshi-weather-calibration" {
				if err := Run(context.Background(), []string{command, "--database-url=postgres://invalid"}, &out, &errOut); err == nil {
					t.Fatal("missing capture inputs admitted")
				}
			} else if err := Run(context.Background(), []string{command, "--database-url=postgres://invalid", "--as-of=invalid"}, &out, &errOut); err == nil {
				t.Fatal("invalid cutoff admitted")
			}
		})
	}
}
