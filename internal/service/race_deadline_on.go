//go:build race

package service

// raceDeadlineScale stretches owner replay request deadlines in race-detector
// builds, which run replay about ten times slower than production builds.
const raceDeadlineScale = 10
