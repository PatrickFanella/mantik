// Package good is an audit fixture: a strategy that only reads its view.
package good

import (
	clock "time"

	"git.subcult.tv/subculture-collective/mantik/internal/strategy"
)

type Hold struct{}

func (Hold) Spec() strategy.Spec {
	return strategy.Spec{Name: "hold", Version: "1", Data: []string{strategy.KindDailyBars}}
}

func (Hold) Decide(v strategy.View) (strategy.Decision, error) {
	_ = v.At().Add(clock.Hour)
	return strategy.Decision{}, nil
}
