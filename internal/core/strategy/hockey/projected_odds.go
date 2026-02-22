package hockey

import "math"

// Model constants fitted to historical hockey data.
// K  = lead coefficient
// A  = time amplification factor
// θ  = time denominator offset (prevents division by zero near game end)
// η  = time exponent base
// λ  = lead decay rate on time exponent
const (
	kCoeff  = 0.55
	aCoeff  = 0.5
	theta   = 4.4
	eta     = 1.0
	lambda_ = 1.5
)

// ProjectedOdds estimates a team's win probability given pregame strength,
// time remaining, and current goal lead. Returns a value in [0, 1].
//
// The first term decays pregame strength toward 50/50 as time runs out.
// The second term amplifies the lead's impact as the clock winds down
// (a 1-goal lead with 5 minutes left is worth more than with 40 minutes left).
func ProjectedOdds(teamStrength, timeRemain, currentLead float64) float64 {
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
	timeFactor := math.Pow(timeRemain/60.0, eta*math.Exp(-lambda_*math.Abs(currentLead)))
	leadTerm := kCoeff * currentLead * (1 + aCoeff*(60.0/(timeRemain+theta)-1))

	exponent := logOdds*timeFactor + leadTerm
	return 1.0 / (1.0 + math.Exp(-exponent))
}
