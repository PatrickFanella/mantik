//go:build race

package service

// raceDeadlineScale stretches owner replay request deadlines in race-detector
// builds, which run replay about ten times slower than production builds.
// Source mutations also replay retained owner envelopes for their causal clock
// floor and need the same instrumentation allowance as decision/outcome reads.
const raceDeadlineScale = 10
