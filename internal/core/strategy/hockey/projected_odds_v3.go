package hockey

import "math"

// Poisson model: instead of a logistic curve with arbitrary constants, model
// hockey as two independent Poisson scoring processes. Given the current score
// and time remaining, enumerate all possible future scorelines and sum
// probabilities exactly. The only parameter is the NHL average total scoring
// rate (~6 goals per 60 min regulation), not a fitted constant.

const (
	poissonTotalRate = 6.0 / 60.0
	poissonMaxGoals  = 15
)

var logFact [poissonMaxGoals + 1]float64

func init() {
	for i := 1; i <= poissonMaxGoals; i++ {
		logFact[i] = logFact[i-1] + math.Log(float64(i))
	}
}

func poissPMF(mu float64, k int) float64 {
	if mu <= 0 {
		if k == 0 {
			return 1.0
		}
		return 0.0
	}
	return math.Exp(float64(k)*math.Log(mu) - mu - logFact[k])
}

// poissonWinProb computes P(team wins) given expected future goals for each
// side and the current integer lead. Ties at end of regulation are split 50/50.
func poissonWinProb(muTeam, muOpp float64, lead int) float64 {
	var pWin, pTie float64
	for x := 0; x <= poissonMaxGoals; x++ {
		px := poissPMF(muTeam, x)
		for y := 0; y <= poissonMaxGoals; y++ {
			final := lead + x - y
			py := poissPMF(muOpp, y)
			if final > 0 {
				pWin += px * py
			} else if final == 0 {
				pTie += px * py
			}
		}
	}
	return pWin + 0.5*pTie
}

// findScoringShare binary-searches for the fraction of total scoring rate
// attributable to a team such that the full-game (60 min, 0-0) Poisson win
// probability matches the pregame odds exactly.
func findScoringShare(pregamePct float64) float64 {
	lo, hi := 0.01, 0.99
	for i := 0; i < 50; i++ {
		mid := (lo + hi) / 2
		mu1 := poissonTotalRate * mid * 60.0
		mu2 := poissonTotalRate * (1 - mid) * 60.0
		if poissonWinProb(mu1, mu2, 0) < pregamePct {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// ProjectedOddsV3 uses a Poisson scoring model. Each team's goals follow an
// independent Poisson process; rates are calibrated so that a full-game
// prediction at 0-0 matches the pregame odds. Given the live score and time
// remaining, the model enumerates all possible future scorelines to compute
// the exact win probability. No fitted constants — just the Poisson distribution
// and the NHL average scoring rate.
func ProjectedOddsV3(teamStrength, timeRemain, currentLead float64) float64 {
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

	share := findScoringShare(strength)
	muTeam := poissonTotalRate * share * timeRemain
	muOpp := poissonTotalRate * (1 - share) * timeRemain
	lead := int(math.Round(currentLead))

	return poissonWinProb(muTeam, muOpp, lead)
}
