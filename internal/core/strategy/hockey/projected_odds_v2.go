package hockey

import "math"

// V2 model constants.
// K  = lead coefficient (increased from 0.55 to better value large leads)
// A  = time amplification factor
// θ  = time denominator offset (prevents division by zero near game end)
// δ  = lead-based pregame decay (replaces λ; pregame washes out faster at large leads)
const (
	kCoeffV2 = 0.80
	aCoeffV2 = 0.5
	thetaV2  = 4.4
	deltaV2  = 0.15
)

// ProjectedOddsV2 estimates a team's win probability given pregame strength,
// time remaining, and current goal lead. Returns a value in [0, 1].
//
// Changes from V1:
//   - timeFactor exponent uses (1 + δ·lead²) instead of η·exp(-λ·|lead|).
//     This makes pregame strength wash out faster at large leads (the scoreboard
//     dominates) instead of being preserved at full weight.
//   - k increased from 0.55 to 0.80 so large leads are valued more decisively.
func ProjectedOddsV2(teamStrength, timeRemain, currentLead float64) float64 {
	strength := math.Max(0.001, math.Min(0.999, teamStrength))

	if timeRemain <= 0 {
		switch {
		case currentLead > 0:
			return 1.0
		case currentLead < 0:
			return 0.0
		default:
			return 0.5
		}
	}

	logOdds := math.Log(strength / (1 - strength))
	timeFactor := math.Pow(timeRemain/60.0, 1.0+deltaV2*currentLead*currentLead)
	leadTerm := kCoeffV2 * currentLead * (1 + aCoeffV2*(60.0/(timeRemain+thetaV2)-1))

	exponent := logOdds*timeFactor + leadTerm
	return 1.0 / (1.0 + math.Exp(-exponent))
}
