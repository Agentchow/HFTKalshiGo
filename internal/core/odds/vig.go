package odds

import "math"

// PregameOdds holds vig-free 1X2 probabilities and expected total goals for one match.
type PregameOdds struct {
	HomeTeam            string
	AwayTeam            string
	HomePregameStrength float64 // 0–1
	DrawPct             float64 // 0–1
	AwayPregameStrength float64 // 0–1
	G0                  float64 // expected total goals
}

// RemoveVig2 converts two-way decimal odds to fair probabilities
// by stripping the bookmaker's overround.
func RemoveVig2(a, b float64) (float64, float64) {
	rawA := 1.0 / a
	rawB := 1.0 / b
	total := rawA + rawB
	return rawA / total, rawB / total
}

// RemoveVig3 converts three-way decimal odds to fair probabilities.
func RemoveVig3(a, b, c float64) (float64, float64, float64) {
	rawA := 1.0 / a
	rawB := 1.0 / b
	rawC := 1.0 / c
	total := rawA + rawB + rawC
	return rawA / total, rawB / total, rawC / total
}

// PoissonCDF2 returns P(X <= 2) for a Poisson distribution with mean g0.
func PoissonCDF2(g0 float64) float64 {
	if g0 <= 0 {
		return 1.0
	}
	return math.Exp(-g0) * (1.0 + g0 + g0*g0/2.0)
}

// InferG0FromOU25 uses binary search to find the expected total goals (g0)
// that produces the given under-2.5 probability via the Poisson CDF.
func InferG0FromOU25(pUnder float64) float64 {
	if pUnder <= 0.01 || pUnder >= 0.99 {
		return 2.5
	}
	lo, hi := 0.1, 8.0
	for range 60 {
		mid := (lo + hi) / 2.0
		if PoissonCDF2(mid) > pUnder {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2.0
}
