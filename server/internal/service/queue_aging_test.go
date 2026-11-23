package service

import "testing"

func TestParsePriorityAgingPerHour(t *testing.T) {
	cases := map[string]float64{
		"":     DefaultPriorityAgingPerHour,
		"  ":   DefaultPriorityAgingPerHour,
		"0":    0,
		"0.5":  0.5,
		" 2 ":  2,
		"-1":   DefaultPriorityAgingPerHour,
		"abc":  DefaultPriorityAgingPerHour,
		"NaN":  DefaultPriorityAgingPerHour,
		"+Inf": DefaultPriorityAgingPerHour,
	}
	for raw, want := range cases {
		if got := parsePriorityAgingPerHour(raw); got != want {
			t.Errorf("parsePriorityAgingPerHour(%q) = %v, want %v", raw, got, want)
		}
	}
}
