package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"git.subcult.tv/subculture-collective/mantik/internal/domain"
)

// FixtureActionGate deliberately stalls synthetic delivery for isolated crash qualification.
// It never grants authority, edits an intent, or changes the worker's lease/clock.
// Normal services leave both environment settings empty.
type FixtureActionGate struct {
	directory string
	stage     string
}

func NewFixtureActionGate(directory, stage string) (*FixtureActionGate, error) {
	if directory == "" && stage == "" {
		return nil, nil
	}
	if !filepath.IsAbs(directory) || (stage != "before_submit" && stage != "after_submit") {
		return nil, errors.New("fixture gate requires an absolute directory and before_submit or after_submit stage")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("fixture gate directory must already exist")
	}
	return &FixtureActionGate{directory: directory, stage: stage}, nil
}

func (g *FixtureActionGate) pause(ctx context.Context, stage string, intent domain.ActionIntent) error {
	if g == nil || stage != g.stage {
		return nil
	}
	if intent.Version != "paper-action-v1" || intent.Quote.Source != "fixture" || intent.Quote.Symbol != "DEMO" {
		return errors.New("fixture gate refuses non-fixture action authority")
	}
	marker := filepath.Join(g.directory, "reached.json")
	payload, err := json.Marshal(struct {
		Stage    string              `json:"stage"`
		At       time.Time           `json:"at"`
		ActionID string              `json:"action_id"`
		Intent   domain.ActionIntent `json:"intent"`
	}{stage, time.Now().UTC(), intent.ID, intent})
	if err != nil {
		return err
	}
	// Exclusive creation prevents a restart or another action from replacing boundary evidence.
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("fixture gate marker: %w", err)
	}
	_, writeErr := file.Write(payload)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(g.directory, "release")); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
