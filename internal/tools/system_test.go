package tools

import "testing"

func TestRoundPercent(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{5.21, 5},
		{59.28, 59},
		{44.43, 44},
		{99.5, 100},
		{0.4, 0},
	}
	for _, c := range cases {
		if got := roundPercent(c.in); got != c.want {
			t.Errorf("roundPercent(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestBytesToGB(t *testing.T) {
	cases := []struct {
		in   uint64
		want float64
	}{
		{7_690_000_000, 7.7},
		{96_750_000_000, 96.8},
		{1_000_000_000, 1},
		{0, 0},
	}
	for _, c := range cases {
		if got := bytesToGB(c.in); got != c.want {
			t.Errorf("bytesToGB(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
