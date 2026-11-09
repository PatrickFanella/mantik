package storage

import (
	"encoding/json"
	"time"
)

// Selection metadata belongs to the original owner epoch. Keep it outside the
// frozen PaperRecordEpoch envelope so existing archive hashes do not change.
type paperRecordScope struct {
	StrategyID     string
	StrategySHA256 string
	DecisionID     string
	DecisionSHA256 string
	DecisionAt     time.Time
}

func paperScopeForDecision(d PaperDecisionRecord) paperRecordScope {
	return paperRecordScope{d.Command.StrategyID, d.Command.StrategySHA256, d.Command.ID, d.SHA256, d.FinalizedAt.UTC()}
}

func paperArchivedScope(r ArchivedPaperRecord) (paperRecordScope, error) {
	switch r.Kind {
	case "decision":
		var d retainedPaperDecision
		if json.Unmarshal(r.Payload, &d) != nil || d.Record.Command.ID != r.ID || d.Record.SHA256 != r.RecordSHA256 || !d.Record.FinalizedAt.Equal(r.At) {
			return paperRecordScope{}, ErrPaperRecordArchiveConflict
		}
		return paperScopeForDecision(d.Record), nil
	case "outcome":
		var o retainedPaperOutcome
		if json.Unmarshal(r.Payload, &o) != nil || o.Record.Command.ID != r.ID || o.Record.SHA256 != r.RecordSHA256 || !o.Record.EvaluatedAt.Equal(r.At) ||
			o.Record.Command.DecisionID != o.Basis.Decision.Command.ID || o.Record.Command.DecisionSHA256 != o.Basis.Decision.SHA256 {
			return paperRecordScope{}, ErrPaperRecordArchiveConflict
		}
		return paperScopeForDecision(o.Basis.Decision), nil
	}
	return paperRecordScope{}, nil
}

func checkPaperRecordScopes(records []ArchivedPaperRecord, scopes map[string]paperRecordScope) error {
	for _, r := range records {
		if r.Kind != "decision" && r.Kind != "outcome" {
			continue
		}
		want, err := paperArchivedScope(r)
		if err != nil {
			return err
		}
		if got, ok := scopes[r.Kind+":"+r.ID]; !ok || got != want {
			return ErrPaperRecordArchiveConflict
		}
	}
	return nil
}
