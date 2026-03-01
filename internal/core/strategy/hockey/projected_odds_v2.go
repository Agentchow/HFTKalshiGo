package hockey

import "math"

// V2 model constants.
// Two changes from the working model:
//  1. timeFactor uses rational decay 1/(1+α·|lead|) instead of exp(-λ·|lead|),
//     so pregame washes out gradually across all lead sizes instead of saturating
//     at lead=2.
//  2. k scales nonlinearly via β at |lead| >= 4 for blowout decisiveness.
const (
	kCoeffV2 = 0.55
	aCoeffV2 = 0.5
	thetaV2  = 4.4
	alphaV2  = 1.5
	betaV2   = 1.16
)

// ProjectedOddsV2 estimates a team's win probability given pregame strength,
// time remaining, and current goal lead. Returns a value in [0, 1].
//
// Changes from working model:
//   - timeFactor exponent uses 1/(1 + α·|lead|) (rational decay) instead of
//     η·exp(-λ·|lead|) (exponential decay). The rational form decays gradually,
//     giving good separation between leads 1, 2, and 3 where the exponential
//     was already saturated.
//   - k scales nonlinearly at |lead| >= 4 via k × (1 + β × max(0, |lead| - 3)).
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

	absLead := math.Abs(currentLead)
	kEff := kCoeffV2 * (1 + betaV2*math.Max(0, absLead-3))

	logOdds := math.Log(strength / (1 - strength))
	timeFactor := math.Pow(timeRemain/60.0, 1.0/(1.0+alphaV2*absLead))
	leadTerm := kEff * currentLead * (1 + aCoeffV2*(60.0/(timeRemain+thetaV2)-1))

	exponent := logOdds*timeFactor + leadTerm
	return 1.0 / (1.0 + math.Exp(-exponent))
}
