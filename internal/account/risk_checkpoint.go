package account

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/risk"
)

// RiskCheckpoint is a store-derived replay artifact for a consecutive run of
// observations. Last preserves the final observation and the rolling hash binds
// every original observation. Immutable originals belong to the store archive.
// ApplyCappedCommand refuses checkpoints supplied as commands.
type RiskCheckpoint struct {
	Count  int64
	SHA256 string
	State  risk.State
	Last   BookCommand
}

// CappedPrefix selects a hot-journal prefix, preserving the original command
// version. It does not copy payloads; callers must treat the result as read-only.
func CappedPrefix(s CappedState, n int) CappedState {
	s.Events = s.Events[:n]
	s.Version = 0
	for _, c := range s.Events {
		s.Version += CappedCommandCount(c)
	}
	return s
}

// CappedCommandCount returns the number of original commands represented by a
// retained event. Ordinary events count once; an internal checkpoint counts its
// archived consecutive observations.
func CappedCommandCount(c BookCommand) int64 {
	if c.Kind == "risk_checkpoint" && c.RiskCheckpoint != nil {
		return c.RiskCheckpoint.Count
	}
	return 1
}

func validateRiskCheckpoint(c BookCommand) error {
	p := c.RiskCheckpoint
	if p == nil || p.Count < 1 || !hashText.MatchString(p.SHA256) || p.Last.Kind != "risk_observe" || p.Last.RiskCheckpoint != nil || p.Last.ID != c.ID || !p.Last.At.Equal(c.At) || !p.State.At.Equal(c.At) {
		return errors.New("invalid retained risk checkpoint")
	}
	return nil
}

// compactTrailingRisk never changes a prefix consumed by an economic event or
// a review hash. Only consecutive observations at the current tail are folded.
// Each Observe still runs, including its rounding, daily baseline and brakes.
func compactTrailingRisk(s CappedState) (CappedState, error) {
	start := len(s.Events)
	for start > 0 {
		c := s.Events[start-1]
		if c.Kind != "risk_observe" && c.Kind != "risk_checkpoint" {
			break
		}
		start--
	}
	if start == len(s.Events) {
		return s, nil
	}
	r, err := replay(s)
	if err != nil {
		return s, err
	}
	p := &RiskCheckpoint{State: r.risk}
	for _, c := range s.Events[start:] {
		if c.Kind == "risk_checkpoint" {
			p.Count += c.RiskCheckpoint.Count
			p.SHA256 = c.RiskCheckpoint.SHA256
			p.Last = c.RiskCheckpoint.Last
		} else {
			raw, err := json.Marshal(c)
			if err != nil {
				return s, err
			}
			sum := sha256.Sum256(append([]byte(p.SHA256), raw...))
			p.SHA256 = hex.EncodeToString(sum[:])
			p.Count++
			p.Last = c
		}
	}
	c := BookCommand{ID: p.Last.ID, Kind: "risk_checkpoint", At: p.Last.At, RiskCheckpoint: p}
	s.Events = append(s.Events[:start:start], c)
	return s, nil
}

// ValidateCappedState replays a retained projection without supplying new marks
// or changing risk state. Recovery callers use it to validate legacy originals.
func ValidateCappedState(s CappedState) error {
	_, err := replay(s)
	return err
}

// CappedVersionPrefix selects a retained hot boundary by its original-command
// version. A cut inside a folded observation run needs immutable history replay.
func CappedVersionPrefix(s CappedState, version int64) (CappedState, error) {
	if version < 0 || version > s.Version {
		return CappedState{}, ErrBookConflict
	}
	var count int64
	for i, c := range s.Events {
		if count == version {
			return CappedPrefix(s, i), nil
		}
		count += CappedCommandCount(c)
	}
	if count == version {
		return CappedPrefix(s, len(s.Events)), nil
	}
	return CappedState{}, ErrBookConflict
}
