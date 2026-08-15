package ytgo

import "testing"

func TestProgressFraction(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
		want float64
	}{
		{"zero", Progress{Cur: 0, Tot: 1000}, 0},
		{"half", Progress{Cur: 500, Tot: 1000}, 0.5},
		{"complete", Progress{Cur: 1000, Tot: 1000}, 1},
		{"unknown total", Progress{Cur: 500, Tot: 0}, -1},
		{"negative total", Progress{Cur: 500, Tot: -1}, -1},
		{"overshoot clamped", Progress{Cur: 1200, Tot: 1000}, 1},
		{"overall preferred", Progress{Cur: 0, Tot: 1000, Overall: 0.94, HasOverall: true}, 0.94},
		{"overall unknown", Progress{Cur: 10, Tot: 100, Overall: -1, HasOverall: true}, -1},
		{"overall clamped", Progress{Cur: 1, Tot: 1, Overall: 1.2, HasOverall: true}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.p.Fraction()
			if got != tt.want {
				t.Errorf("Fraction() = %v, want %v", got, tt.want)
			}
		})
	}
}
