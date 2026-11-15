package marketdata

import "testing"

func TestCompareCloses(t *testing.T) {
	for _, tc := range []struct {
		name          string
		change        func(*Series, *[]ReferenceClose, *[]string)
		pass, invalid bool
	}{
		{name: "within_tolerance", pass: true},
		{name: "return_difference", change: func(s *Series, _ *[]ReferenceClose, _ *[]string) { s.Bars[1].TotalReturn = "1.02" }},
		{name: "difference", change: func(s *Series, _ *[]ReferenceClose, _ *[]string) { s.Bars[0].Close = "102" }},
		{name: "missing_middle", change: func(s *Series, _ *[]ReferenceClose, _ *[]string) { s.Bars = s.Bars[1:] }},
		{name: "reference_gap", change: func(_ *Series, r *[]ReferenceClose, _ *[]string) { *r = (*r)[1:] }},
		{name: "extra", change: func(s *Series, _ *[]ReferenceClose, _ *[]string) {
			s.Bars = append(s.Bars, AdjustedBar{Date: "2016-01-06", Close: "100"})
		}},
		{name: "duplicate", change: func(_ *Series, r *[]ReferenceClose, _ *[]string) { *r = append(*r, (*r)[0]) }, invalid: true},
		{name: "invalid", change: func(_ *Series, r *[]ReferenceClose, _ *[]string) { (*r)[0].Close = "NaN" }, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Series{Bars: []AdjustedBar{{Date: "2016-01-04", Close: "100.001", TotalReturn: "1"}, {Date: "2016-01-05", Close: "101", TotalReturn: "1.01"}}}
			r := []ReferenceClose{{Date: "2016-01-04", Close: "100"}, {Date: "2016-01-05", Close: "101"}}
			c := []string{"2016-01-04", "2016-01-05"}
			if tc.change != nil {
				tc.change(&s, &r, &c)
			}
			report, err := CompareCloses(s, r, c, "5", "1.01")
			if (err != nil) != tc.invalid || err == nil && report.Passed != tc.pass {
				t.Fatal(report, err)
			}
		})
	}
}
