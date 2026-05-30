package ytgo

import "testing"

func TestProgressFraction(t *testing.T) {
	tests := []struct {
		name     string
		cur, tot int64
		want     float64
	}{
		{"zero", 0, 1000, 0},
		{"half", 500, 1000, 0.5},
		{"complete", 1000, 1000, 1},
		{"unknown total", 500, 0, -1},
		{"negative total", 500, -1, -1},
		{"overshoot clamped", 1200, 1000, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Progress{Cur: tt.cur, Tot: tt.tot}.Fraction()
			if got != tt.want {
				t.Errorf("Fraction() = %v, want %v", got, tt.want)
			}
		})
	}
}
