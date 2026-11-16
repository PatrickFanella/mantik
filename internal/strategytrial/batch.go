package strategytrial

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrFamilyPending means retained proposals have not yet been reconciled with
// registry starts. No finite search count or family snapshot is authoritative.
var ErrFamilyPending = errors.New("strategy family has unreconciled proposals")

// Batch retains exact private reply bytes before the reply journal write. IDs
// are deterministic attempts derived by the desk, never a caller-supplied N.
// Acknowledgement loss never removes this fence; exact retries reuse its bytes.
type Batch struct {
	ID       string          `json:"id"`
	Family   string          `json:"family"`
	Reply    json.RawMessage `json:"reply"`
	At       time.Time       `json:"at"`
	TrialIDs []string        `json:"trial_ids"`
}

// BatchStore fences a family before retaining a proposal-containing reply.
// ResolveStrategyTrialBatch is called only after successful exact dispatch and
// verifies every declared start exists in this family. It grants no authority.
type BatchStore interface {
	BeginStrategyTrialBatch(context.Context, string, string) error
	ReserveStrategyTrialBatch(context.Context, Batch) error
	StrategyTrialBatch(context.Context, string) (Batch, error)
	ResolveStrategyTrialBatch(context.Context, string) error
}
