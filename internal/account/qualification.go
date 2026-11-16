package account

import (
	"errors"

	"git.subcult.tv/subculture-collective/mantik/internal/forward"
	"git.subcult.tv/subculture-collective/mantik/internal/workerplan"
)

// Qualification returns retained automatic admission. A subsequent overseer
// demotion is terminal for this grant; growth cannot revive qualification.
func Qualification(s CappedState, sleeve string) *forward.Assessment {
	var out *forward.Assessment
	for _, e := range s.Events {
		if e.Qualification != nil && e.Qualification.SleeveID == sleeve && e.Qualification.Status == "passed" {
			out = e.Qualification
		}
		if e.Review != nil && e.Review.SleeveID == sleeve && (e.Review.Action == "demote" || e.Review.Action == "retire") {
			out = nil
		}
	}
	return out
}
func replayQualification(s CappedState, c BookCommand, r *bookReplay) error {
	q := c.Qualification
	if q == nil || q.Status != "passed" && q.Status != "failed" || c.ID != "qualification-"+q.PlanID || !c.At.Equal(q.At) || q.Policy != forward.AssessmentPolicy || q.QualificationCredit != (q.Status == "passed") {
		return errors.New("invalid qualification event")
	}
	prefix := reviewPrefix(s, c.ID)
	if prefix.Version != q.JournalVersion || workerplan.Hash(prefix) != q.JournalSHA256 {
		return ErrBookConflict
	}
	copy := *q
	copy.SHA256 = ""
	if workerplan.Hash(copy) != q.SHA256 {
		return errors.New("qualification digest differs")
	}
	if q.Status == "passed" {
		owner, ok := r.sleeves[q.SleeveID]
		if !ok || owner.StrategyID != q.StrategyID {
			return errors.New("qualification owner differs")
		}
		owner.Stage = "qualified"
		r.sleeves[q.SleeveID] = owner
	}
	return nil
}

// CheckQualificationControls reads retained owner and hard-halt state without
// manufacturing marks for unrelated positions. Every later order still runs
// the current mark-based hard risk check at allocation and submission.
func CheckQualificationControls(s CappedState, strategyID string) error {
	r, e := replay(s)
	if e != nil {
		return e
	}
	if r.halted || r.risk.Halted || r.risk.Paused || r.risk.Kill || r.risk.ReduceOnly || r.risk.Breakers[strategyID] || r.risk.Retired[strategyID] {
		return errors.New("owner_or_hard_risk_control")
	}
	return nil
}

// PendingQualificationOrders refuses a closed-window conclusion while an owned
// order can still fill, including an unfilled order with no position yet.
func PendingQualificationOrders(s CappedState, sleeve string) (bool, error) {
	r, e := replay(s)
	if e != nil {
		return false, e
	}
	for _, o := range r.orders {
		if o.order.SleeveID != sleeve || o.released {
			continue
		}
		full, _ := bookRat(o.order.Quantity)
		if o.filled.Cmp(full) < 0 {
			return true, nil
		}
	}
	return false, nil
}
