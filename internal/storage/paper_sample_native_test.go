package storage

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPaperSampleNativeGuards(t *testing.T) {
	m := NewMemory()
	de := func(PaperDecisionCommand, PaperDecisionBasis, time.Time) (PaperDecisionEvaluation, error) {
		t.Fatal("oversized page reached native evaluation")
		return PaperDecisionEvaluation{}, nil
	}
	oe := func(PaperOutcomeBasis, time.Time) (PaperOutcomeEvaluation, error) {
		t.Fatal("oversized page reached native evaluation")
		return PaperOutcomeEvaluation{}, nil
	}
	if got, err := m.paperSampleNativeLocked(t.Context(), m.receiverID, make([]paperSampleRecord, maxPaperNativePage+1), de, oe); !errors.Is(err, ErrPaperRecordArchiveInvalid) || !reflect.DeepEqual(got, paperArchiveOwners{}) {
		t.Fatal("oversized page accepted or returned partial owners", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := m.paperSampleNativeLocked(ctx, m.receiverID, nil, de, oe); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, paperArchiveOwners{}) {
		t.Fatal("cancelled native page returned success or partial owners", err)
	}
}
