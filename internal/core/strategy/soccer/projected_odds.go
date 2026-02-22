package soccer

import "math"

const maxGoals = 12

// ThreeWayProbs holds home-win / draw / away-win probabilities (0–1).
type ThreeWayProbs struct {
	Home float64
	Draw float64
	Away float64
}

// InferLambdas finds (λ_home, λ_away) such that λ_home + λ_away = g0
// and the Poisson-implied 1X2 probabilities best match the pregame 1X2.
// Uses a dense 1-D grid search over λ_home ∈ (ε, g0−ε).
func InferLambdas(homeP, drawP, awayP, g0 float64) (float64, float64) {
	total := homeP + drawP + awayP
	if total <= 0 || g0 <= 1e-9 {
		return g0 / 2, g0 / 2
	}
	hp, dp, ap := homeP/total, drawP/total, awayP/total

	const gridSteps = 400
	eps := 1e-6
	bestX := g0 / 2
	bestErr := math.Inf(1)

	for s := 1; s < gridSteps; s++ {
		x := eps + (g0-2*eps)*float64(s)/gridSteps
		mh, md, ma := fullMatchProbs(x, g0-x)

		err := (mh-hp)*(mh-hp) + (md-dp)*(md-dp) + (ma-ap)*(ma-ap)
		if err < bestErr {
			bestErr = err
			bestX = x
		}
	}
	return bestX, g0 - bestX
}

// stoppageBuffer prevents the model from going fully deterministic while
// a match is still live. Real matches have 3-8 min of stoppage time, so
// we never let effective remaining time fall below this floor.
const stoppageBuffer = 3.0 // minutes

// InplayProbs computes live (home, draw, away) probabilities given:
//   - lamHome, lamAway: full-match goal rates (from InferLambdas)
//   - timeRemain: minutes remaining in regulation (0–90)
//   - goalDiff: homeScore − awayScore
//   - redsHome, redsAway: count of red cards per side
//   - half: 1 or 2
//   - isLive: true if the match is still being played (enables stoppage buffer)
func InplayProbs(lamHome, lamAway, timeRemain float64, goalDiff, half, redsHome, redsAway int, isLive bool) ThreeWayProbs {
	effective := effectiveTimeRemaining(timeRemain, isLive)

	if effective <= 0 {
		switch {
		case goalDiff > 0:
			return ThreeWayProbs{Home: 1, Draw: 0, Away: 0}
		case goalDiff < 0:
			return ThreeWayProbs{Home: 0, Draw: 0, Away: 1}
		default:
			return ThreeWayProbs{Home: 0, Draw: 1, Away: 0}
		}
	}

	muH, muA := remainingRates(lamHome, lamAway, effective)
	muH, muA = applyDynamic(muH, muA, goalDiff, half, effective)
	muH, muA = applyRedCards(muH, muA, redsHome, redsAway)

	probs := skellamProbs(muH, muA, goalDiff)
	return dixonColesCorrection(probs, muH, muA, goalDiff)
}

// OverUnderProb returns P(total goals > line) given current state.
func OverUnderProb(lamHome, lamAway, timeRemain float64, currentTotal int, line float64, half, redsHome, redsAway int, isLive bool) float64 {
	effective := effectiveTimeRemaining(timeRemain, isLive)

	if effective <= 0 {
		if float64(currentTotal) > line {
			return 1.0
		}
		return 0.0
	}

	muH, muA := remainingRates(lamHome, lamAway, effective)
	muH, muA = applyDynamic(muH, muA, 0, half, effective)
	muH, muA = applyRedCards(muH, muA, redsHome, redsAway)

	remaining := line - float64(currentTotal)
	if remaining < 0 {
		return 1.0
	}

	threshold := int(remaining)
	return 1.0 - poissonCDF(threshold, muH+muA)
}

// effectiveTimeRemaining applies the stoppage time buffer: when a match
// is live and the clock shows 0 or negative, we clamp to stoppageBuffer
// so the model never snaps to deterministic 100%/0%.
func effectiveTimeRemaining(timeRemain float64, isLive bool) float64 {
	if isLive && timeRemain < stoppageBuffer {
		return stoppageBuffer
	}
	return clamp(timeRemain, 0, 90)
}

// remainingRates scales full-match lambdas to remaining-time rates,
// using a non-uniform intensity profile. Goals cluster in the final
// 15 minutes (~30-40% higher intensity than the opening). We model
// this as a piecewise-linear intensity that ramps from 0.85 to 1.30
// over 90 minutes, normalizing so the integral over [0,90] = 1.
func remainingRates(lamH, lamA, timeRemain float64) (float64, float64) {
	played := 90.0 - timeRemain
	fracRemaining := intensityIntegral(90.0) - intensityIntegral(played)
	return lamH * fracRemaining, lamA * fracRemaining
}

// intensityIntegral returns the cumulative fraction of expected goals
// scored by minute t. The rate profile linearly ramps from
// baseRate at t=0 to peakRate at t=90, normalized so integral(0,90)=1.
func intensityIntegral(t float64) float64 {
	const (
		baseRate = 0.85
		peakRate = 1.30
	)
	// rate(t) = baseRate + (peakRate-baseRate)*t/90
	// integral(0,T) = baseRate*T + (peakRate-baseRate)*T^2/(2*90)
	// Normalize by integral(0,90) = baseRate*90 + (peakRate-baseRate)*45
	//                             = 0.85*90 + 0.45*45 = 76.5 + 20.25 = 96.75
	const norm = 96.75
	raw := baseRate*t + (peakRate-baseRate)*t*t/(2.0*90.0)
	return raw / norm
}

// --- Dynamic intensity adjustments ---

// applyDynamic adjusts remaining scoring rates based on score deficit, half,
// and time remaining. Trailing-team urgency scales with elapsed time:
// minimal early, maximal in the last 15 minutes.
func applyDynamic(muH, muA float64, goalDiff, half int, timeRemain float64) (float64, float64) {
	urgency := urgencyFactor(timeRemain)

	switch {
	case goalDiff > 0:
		leadDampen := 1.0 - 0.20*urgency // leading team eases off more as time runs out
		muH *= leadDampen
		if goalDiff == 1 {
			muA *= 1.0 + 0.10*urgency
		} else {
			muA *= 1.0 + 0.20*urgency
		}
	case goalDiff < 0:
		leadDampen := 1.0 - 0.20*urgency
		muA *= leadDampen
		if goalDiff == -1 {
			muH *= 1.0 + 0.10*urgency
		} else {
			muH *= 1.0 + 0.20*urgency
		}
	}

	if half >= 2 {
		muH *= 1.07
		muA *= 1.07
	}

	return muH, muA
}

// urgencyFactor returns 0 at minute 0, 1 at minute 90. Captures that
// tactical shifts (park the bus, all-out attack) intensify as the match
// progresses — a team trailing 0-1 in the 5th minute barely changes
// tactics, but trailing 0-1 in the 80th minute goes all-out.
func urgencyFactor(timeRemain float64) float64 {
	played := 90.0 - clamp(timeRemain, 0, 90)
	return played / 90.0
}

// applyRedCards reduces scoring rate for the team with reds and increases
// the opponent's. Per academic literature (Vecer 2009, Goddard &
// Asimakopoulos 2004), a red card reduces scoring rate by ~20-25%.
func applyRedCards(muH, muA float64, redsH, redsA int) (float64, float64) {
	const rho = 0.25 // per-card dampening (academic consensus: 20-25%)
	for i := 0; i < redsH; i++ {
		muH *= (1 - rho)
		muA *= (1 + rho*0.5)
	}
	for i := 0; i < redsA; i++ {
		muA *= (1 - rho)
		muH *= (1 + rho*0.5)
	}
	return muH, muA
}

// --- Dixon-Coles low-score correction ---

// dixonColesCorrection adjusts for the well-documented tendency of
// independent Poisson to overestimate draw probability in low-scoring
// matches (Dixon & Coles 1997). The tau factor modifies P(0-0), P(1-0),
// P(0-1), and P(1-1) scorelines. We apply a simplified version using
// a fixed rho parameter estimated from large-sample studies (~-0.04).
func dixonColesCorrection(probs ThreeWayProbs, muH, muA float64, goalDiff int) ThreeWayProbs {
	const dcRho = -0.04

	p00 := poissonPMF(0, muH) * poissonPMF(0, muA)
	p10 := poissonPMF(1, muH) * poissonPMF(0, muA)
	p01 := poissonPMF(0, muH) * poissonPMF(1, muA)
	p11 := poissonPMF(1, muH) * poissonPMF(1, muA)

	// tau adjustments from Dixon-Coles (1997)
	tau00 := 1.0 - muH*muA*dcRho
	tau10 := 1.0 + muA*dcRho
	tau01 := 1.0 + muH*dcRho
	tau11 := 1.0 - dcRho

	// Net probability mass shift for draws vs non-draws
	drawShift := p00*(tau00-1.0) + p11*(tau11-1.0)
	homeShift := p10 * (tau10 - 1.0)
	awayShift := p01 * (tau01 - 1.0)

	// These shifts are on the "remaining goals" scorelines, but their effect
	// on the 3-way outcome depends on the current goal difference. When
	// goalDiff=0, (0,0) and (1,1) remaining are draws, (1,0) is home win.
	// For simplicity we apply the correction at the 3-way level — the
	// effect is dominated by the draw deflation.
	adjusted := ThreeWayProbs{
		Home: probs.Home + homeShift,
		Draw: probs.Draw + drawShift,
		Away: probs.Away + awayShift,
	}

	// Clamp and renormalize
	adjusted.Home = math.Max(0, adjusted.Home)
	adjusted.Draw = math.Max(0, adjusted.Draw)
	adjusted.Away = math.Max(0, adjusted.Away)

	total := adjusted.Home + adjusted.Draw + adjusted.Away
	if total > 0 {
		adjusted.Home /= total
		adjusted.Draw /= total
		adjusted.Away /= total
	}

	return adjusted
}

// --- Poisson / Skellam math ---

func skellamProbs(muH, muA float64, goalDiff int) ThreeWayProbs {
	pH := make([]float64, maxGoals+1)
	pA := make([]float64, maxGoals+1)
	for i := 0; i <= maxGoals; i++ {
		pH[i] = poissonPMF(i, muH)
		pA[i] = poissonPMF(i, muA)
	}

	var pHome, pDraw, pAway float64
	for i := 0; i <= maxGoals; i++ {
		for j := 0; j <= maxGoals; j++ {
			p := pH[i] * pA[j]
			final := goalDiff + (i - j)
			switch {
			case final > 0:
				pHome += p
			case final == 0:
				pDraw += p
			default:
				pAway += p
			}
		}
	}

	total := pHome + pDraw + pAway
	if total > 0 {
		pHome /= total
		pDraw /= total
		pAway /= total
	}

	return ThreeWayProbs{Home: pHome, Draw: pDraw, Away: pAway}
}

func fullMatchProbs(lamH, lamA float64) (float64, float64, float64) {
	pH := make([]float64, maxGoals+1)
	pA := make([]float64, maxGoals+1)
	for i := 0; i <= maxGoals; i++ {
		pH[i] = poissonPMF(i, lamH)
		pA[i] = poissonPMF(i, lamA)
	}

	var pHome, pDraw, pAway float64
	for i := 0; i <= maxGoals; i++ {
		for j := 0; j <= maxGoals; j++ {
			p := pH[i] * pA[j]
			switch {
			case i > j:
				pHome += p
			case i == j:
				pDraw += p
			default:
				pAway += p
			}
		}
	}

	total := pHome + pDraw + pAway
	if total > 0 {
		pHome /= total
		pDraw /= total
		pAway /= total
	}
	return pHome, pDraw, pAway
}

func poissonPMF(k int, lambda float64) float64 {
	if lambda <= 0 {
		if k == 0 {
			return 1.0
		}
		return 0.0
	}
	logP := float64(k)*math.Log(lambda) - lambda - logFactorial(k)
	return math.Exp(logP)
}

func poissonCDF(k int, lambda float64) float64 {
	sum := 0.0
	for i := 0; i <= k; i++ {
		sum += poissonPMF(i, lambda)
	}
	return sum
}

func logFactorial(n int) float64 {
	if n <= 1 {
		return 0
	}
	sum := 0.0
	for i := 2; i <= n; i++ {
		sum += math.Log(float64(i))
	}
	return sum
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
