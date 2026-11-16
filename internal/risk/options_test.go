package risk

import (
	"slices"
	"testing"
)

func TestOptionGreekCapsBoundariesAndEveryReason(t *testing.T) {
	// Spread each aggregate cap across underlyings to isolate portfolio caps.
	atCap := []OptionExposure{{"a", "100", "25", "50", "10", "100"}, {"b", "100", "25", "50", "10", "100"}}
	reasons, err := CheckOptions("1000", atCap)
	if err != nil || len(reasons) != 0 {
		t.Fatal(reasons, err)
	}
	for name, field := range map[string]int{"delta": 0, "gamma": 1, "vega": 2, "theta": 3} {
		t.Run(name, func(t *testing.T) {
			v := slices.Clone(atCap)
			switch field {
			case 0:
				v[0].Delta = "100.01"
			case 1:
				v[0].Gamma = "25.01"
			case 2:
				v[0].Vega = "50.01"
			case 3:
				v[0].Theta = "10.01"
			}
			reasons, err := CheckOptions("1000", v)
			if err != nil || !slices.Contains(reasons, "risk_option_"+name) {
				t.Fatal(reasons, err)
			}
		})
	}
	all := []OptionExposure{{"a", "201", "51", "101", "21", "101"}}
	reasons, err = CheckOptions("1000", all)
	if err != nil || !slices.Equal(reasons, []string{"risk_option_delta", "risk_option_gamma", "risk_option_vega", "risk_option_theta", "risk_option_underlying:a"}) {
		t.Fatal(reasons, err)
	}
	// Independent orders and live holdings cannot net or each consume the same cap.
	v := []OptionExposure{{"a", "60", "0", "0", "0", "0"}, {"a", "40.01", "0", "0", "0", "0"}}
	reasons, err = CheckOptions("1000", v)
	if err != nil || !slices.Contains(reasons, "risk_option_underlying:a") {
		t.Fatal(reasons, err)
	}
	for _, v := range []OptionExposure{{"a", "", "0", "0", "0", "0"}, {"a", "-1", "0", "0", "0", "0"}, {"a", "0", "NaN", "0", "0", "0"}, {"", "0", "0", "0", "0", "0"}} {
		if _, err := CheckOptions("1000", []OptionExposure{v}); err == nil {
			t.Fatal("invalid exposure admitted", v)
		}
	}
	for _, eq := range []string{"0", "-1", "", "NaN"} {
		if _, err := CheckOptions(eq, atCap); err == nil {
			t.Fatal("equity", eq)
		}
	}
}
