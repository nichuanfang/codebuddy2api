package service

import "testing"

func TestAsStringPreservesIntegralJSONNumbers(t *testing.T) {
	for _, tc := range []struct {
		value float64
		want  string
	}{
		{10, "10"},
		{100, "100"},
		{10.5, "10.5"},
		{0, "0"},
	} {
		if got := asString(tc.value); got != tc.want {
			t.Fatalf("asString(%v)=%q, want %q", tc.value, got, tc.want)
		}
	}
}
