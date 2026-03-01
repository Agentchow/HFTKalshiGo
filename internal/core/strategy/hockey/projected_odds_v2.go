package hockey

import "math"

// ProjectedOddsV2 uses the same core formula as V1 (exponential decay on
// the time exponent) but adds hard floors for large leads: a team up by 3+
// goals is given at least 92% win probability, and 4+ goals at least 99%.
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
	timeFactor := math.Pow(timeRemain/60.0, eta*math.Exp(-lambda_*math.Abs(currentLead)))
	leadTerm := kCoeff * currentLead * (1 + aCoeff*(60.0/(timeRemain+theta)-1))

	exponent := logOdds*timeFactor + leadTerm
	p := 1.0 / (1.0 + math.Exp(-exponent))

	// Hard floors for large leads.
	if currentLead >= 4 {
		p = math.Max(p, 0.99)
	} else if currentLead >= 3 {
		p = math.Max(p, 0.92)
	} else if currentLead <= -4 {
		p = math.Min(p, 0.01)
	} else if currentLead <= -3 {
		p = math.Min(p, 0.08)
	}

	return p
}
