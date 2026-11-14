package engine

import (
	"fmt"
	"math/big"
	"time"
)

// Deposits fall on Fridays at 06:00 in Chicago, before the market opens.
const (
	depositWeekday = time.Friday
	depositHour    = 6
)

var chicago = mustZone("America/Chicago")

// DepositSchedule adds Amount of cash every EveryWeeks weeks, on Fridays at
// 06:00 America/Chicago wall-clock time, starting with the first such Friday
// at or after the book opens.
type DepositSchedule struct {
	Amount     string `json:"amount"`
	EveryWeeks int    `json:"every_weeks"`
}

// DefaultDeposits is $500 every other Friday.
var DefaultDeposits = DepositSchedule{Amount: "500", EveryWeeks: 2}

// Deposit is one scheduled capital flow. ID is keyed by the scheduled date,
// so a replay or late run can apply it exactly once. AppliedOn is the session
// whose open it arrived before; EffectiveAt stays the scheduled time.
type Deposit struct {
	ID          string    `json:"id"`
	EffectiveAt time.Time `json:"effective_at"`
	Amount      string    `json:"amount"`
	AppliedOn   string    `json:"applied_on,omitempty"`
}

func (s DepositSchedule) parse() (*big.Rat, error) {
	amount, ok := decimal(s.Amount)
	if !ok || amount.Sign() <= 0 || s.EveryWeeks < 1 || s.EveryWeeks > 52 {
		return nil, fmt.Errorf("%w: deposit schedule", ErrInvalidConfig)
	}
	return amount, nil
}

// Due lists the deposits scheduled at or after opened and at or before
// through, in order. Each falls at 06:00 Chicago time on its Friday, so the
// UTC time moves with daylight saving and the wall-clock time does not.
func (s DepositSchedule) Due(opened, through time.Time) ([]Deposit, error) {
	if _, err := s.parse(); err != nil {
		return nil, err
	}
	local := opened.In(chicago)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, chicago)
	for day.Weekday() != depositWeekday || depositTime(day).Before(opened) {
		day = day.AddDate(0, 0, 1)
	}
	out := []Deposit{}
	for at := depositTime(day); !at.After(through); at = depositTime(day) {
		out = append(out, Deposit{ID: "deposit-" + day.Format(time.DateOnly), EffectiveAt: at.UTC(), Amount: s.Amount})
		day = day.AddDate(0, 0, 7*s.EveryWeeks)
	}
	return out, nil
}

func depositTime(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), depositHour, 0, 0, 0, chicago)
}
