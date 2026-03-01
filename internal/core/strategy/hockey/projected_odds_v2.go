package hockey

import "math"

// V2 model constants.
// Identical to working model for leads 1–3. At |lead| >= 4, k scales
// nonlinearly via β to make blowout leads more decisive.
const (
	kCoeffV2 = 0.55
	aCoeffV2 = 0.5
	thetaV2  = 4.4
	etaV2    = 1.0
	lambdaV2 = 1.5
	betaV2   = 1.16
)

// ProjectedOddsV2 estimates a team's win probability given pregame strength,
// time remaining, and current goal lead. Returns a value in [0, 1].
//
// Change from working model: k scales nonlinearly at large leads via
//
//	k_eff = k × (1 + β × max(0, |lead| - 3))
//
// Leads 1–3 are identical to the working model. At 4+ the lead coefficient
// ramps up so blowout scorelines are valued more decisively.
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
	timeFactor := math.Pow(timeRemain/60.0, etaV2*math.Exp(-lambdaV2*absLead))
	leadTerm := kEff * currentLead * (1 + aCoeffV2*(60.0/(timeRemain+thetaV2)-1))

	exponent := logOdds*timeFactor + leadTerm
	return 1.0 / (1.0 + math.Exp(-exponent))
}
