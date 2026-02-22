package strategy

// EffectiveThreshold computes the edge threshold at the current game time.
//
// Near the end of a game, the required edge ramps down from the full
// threshold to zero. This reflects increasing certainty: a 1-goal lead
// with 2 minutes left is nearly decisive, so even a small edge is worth
// taking. At game end (timeRemainMin <= 0), any available liquidity triggers.
//
// rampSec controls how many seconds before the end the ramp begins
// (default 300 = last 5 minutes).
func EffectiveThreshold(timeRemainMin float64, basePct float64, rampSec float64) float64 {
	if rampSec <= 0 {
		rampSec = 300.0
	}
	if timeRemainMin <= 0 {
		return 0.0
	}
	remainSec := timeRemainMin * 60.0
	if remainSec >= rampSec {
		return basePct
	}
	return basePct * (remainSec / rampSec)
}
