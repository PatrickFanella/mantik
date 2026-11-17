package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/kalshi"
	"git.subcult.tv/subculture-collective/mantik/internal/storage"
)

func TestSealedForecastPayloadKeepsItsExactID(t *testing.T) {
	p := kalshi.WeatherPrediction{ModelVersion: kalshi.WeatherModelVersion, MarketTicker: "KXTEST", Probability: .65, Evidence: []kalshi.WeatherEvidence{{Original: `{"text":"source with < and &"}`}}}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	input := kalshi.PaperCheckInput{Prediction: storage.KalshiWeatherPrediction{ID: hex.EncodeToString(sum[:]), Payload: raw}}
	path := filepath.Join(t.TempDir(), "input.json")
	if err := save(path, input); err != nil {
		t.Fatal(err)
	}
	var restored kalshi.PaperCheckInput
	if _, err := read(path, &restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, restored.Prediction.Payload) {
		t.Fatal("serialization changed content-addressed forecast")
	}
	if err := save(path, input); err == nil {
		t.Fatal("immutable input overwritten")
	}
}

func TestPredictionCaptureIsPreCloseAndDurable(t *testing.T) {
	root := t.TempDir()
	s := &captureStore{Memory: storage.NewMemory(), root: root}
	p := storage.KalshiWeatherPrediction{AsOf: time.Now().UTC().Add(-time.Minute), CloseTime: time.Now().UTC().Add(time.Hour)}
	retained, err := s.RecordKalshiWeatherPrediction(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	var readback storage.KalshiWeatherPrediction
	if _, err := read(filepath.Join(root, "prediction.json"), &readback); err != nil {
		t.Fatal(err)
	}
	if !readback.RecordedAt.Equal(retained.RecordedAt) || !retained.RecordedAt.Before(p.CloseTime) {
		t.Fatal(readback, retained)
	}
	if _, err := s.RecordKalshiWeatherPrediction(context.Background(), p); err == nil {
		t.Fatal("forecast original overwritten")
	}
	late := &captureStore{Memory: storage.NewMemory(), root: t.TempDir()}
	p.CloseTime = time.Now().UTC().Add(-time.Second)
	if _, err := late.RecordKalshiWeatherPrediction(context.Background(), p); err == nil {
		t.Fatal("late forecast admitted")
	}
	if _, err := os.Stat(filepath.Join(late.root, "prediction.json")); !os.IsNotExist(err) {
		t.Fatal("late forecast left admitted receipt", err)
	}
}

func TestShowRequiresExactInputPinBeforeAnyProviderRead(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := save(filepath.Join(root, "input.json"), kalshi.PaperCheckInput{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-action", "show", "-root", root, "-input-sha256", "wrong"}, &out, &out); err == nil || err.Error() != "sealed input hash differs" {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"-help"}, &out, &out); err != nil {
		t.Fatal(err)
	}
}

func TestForecastCrossingCloseLeavesOnlyNonAdmittedAttempt(t *testing.T) {
	for _, crossAt := range []int{2, 3} {
		t.Run(strconv.Itoa(crossAt), func(t *testing.T) {
			close := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)
			calls := 0
			s := &captureStore{Memory: storage.NewMemory(), root: t.TempDir(), now: func() time.Time {
				calls++
				if calls >= crossAt {
					return close
				}
				return close.Add(-time.Second)
			}}
			_, err := s.RecordKalshiWeatherPrediction(context.Background(), storage.KalshiWeatherPrediction{AsOf: close.Add(-time.Minute), CloseTime: close})
			if err == nil {
				t.Fatal("late durable publication admitted")
			}
			if _, err := os.Stat(filepath.Join(s.root, "prediction.json")); !os.IsNotExist(err) {
				t.Fatal("late admission link remained", err)
			}
			if _, err := os.Stat(filepath.Join(s.root, "prediction-attempt.json")); err != nil {
				t.Fatal("failed attempt evidence lost", err)
			}
		})
	}
}
