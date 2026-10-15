package trial

import "git.subcult.tv/subculture-collective/mantik/internal/hypothesis"

// Evidence is a targeted, consistent read of retained research records. Raw
// result text stays internal; public assessments expose its digest and metrics.
type Evidence struct {
	Attempt      Attempt
	Registration Registration
	Publication  hypothesis.Publication
	Run          *Run
	Result       string
}
