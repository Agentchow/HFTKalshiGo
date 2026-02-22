package goalserve

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PregameOdds holds vig-free pregame probabilities for a match.
type PregameOdds struct {
	HomeTeam string
	AwayTeam string
	GameID   string
	GameTime string

	HomeWinPct float64 // 0–1
	DrawPct    float64 // 0–1, hockey = 0
	AwayWinPct float64 // 0–1

	ExpectedTotalGoals float64 // lambda_home + lambda_away
}

// --- Hockey moneyline ---

type hockeyOddsXML struct {
	Matches []hockeyMatchXML `xml:"match"`
}

type hockeyMatchXML struct {
	ID        string                `xml:"id,attr"`
	Date      string                `xml:"date,attr"`
	Time      string                `xml:"time,attr"`
	HomeTeam  hockeyTeamXML         `xml:"hometeam"`
	AwayTeam  hockeyTeamXML         `xml:"awayteam"`
	Bookmaker []hockeyBookmakerXML  `xml:"bookmakers>bookmaker"`
}

type hockeyTeamXML struct {
	Name string `xml:"name,attr"`
}

type hockeyBookmakerXML struct {
	Name string        `xml:"name,attr"`
	Odds []hockeyOddXML `xml:"odd"`
}

type hockeyOddXML struct {
	Name  string `xml:"name,attr"`
	Value string `xml:",chardata"`
}

// FetchHockeyOdds returns pregame moneyline probabilities for the given league.
// Preferred bookmaker order: Pinnacle, Bet365, 1xBet.
func (c *Client) FetchHockeyOdds(ctx context.Context, league string) ([]PregameOdds, error) {
	url := c.oddsURL("hockey", league, "odds")

	var feed struct {
		Category hockeyOddsXML `xml:"category"`
	}
	if err := c.fetchXML(ctx, url, &feed); err != nil {
		return nil, fmt.Errorf("fetch hockey odds %s: %w", league, err)
	}

	var results []PregameOdds
	preferred := []string{"Pinnacle", "Pinnacle Sports", "Bet365", "1xBet"}

	for _, m := range feed.Category.Matches {
		bm := pickBookmaker(m.Bookmaker, preferred)
		if bm == nil {
			continue
		}

		var homeML, awayML float64
		for _, odd := range bm.Odds {
			v, err := strconv.ParseFloat(strings.TrimSpace(odd.Value), 64)
			if err != nil {
				continue
			}
			switch strings.ToLower(odd.Name) {
			case "1", "home":
				homeML = v
			case "2", "away":
				awayML = v
			}
		}
		if homeML == 0 || awayML == 0 {
			continue
		}

		hp, ap := moneylineToProb(homeML, awayML)
		results = append(results, PregameOdds{
			HomeTeam:           m.HomeTeam.Name,
			AwayTeam:           m.AwayTeam.Name,
			GameID:             m.ID,
			GameTime:           m.Time,
			HomeWinPct:         hp,
			AwayWinPct:         ap,
			ExpectedTotalGoals: 5.5, // hockey default
		})
	}

	return results, nil
}

// --- Soccer 1X2 + over/under ---

type soccerOddsXML struct {
	Matches []soccerMatchXML `xml:"match"`
}

type soccerMatchXML struct {
	ID        string               `xml:"id,attr"`
	Date      string               `xml:"date,attr"`
	Time      string               `xml:"time,attr"`
	HomeTeam  soccerTeamXML        `xml:"localteam"`
	AwayTeam  soccerTeamXML        `xml:"visitorteam"`
	Bookmaker []soccerBookmakerXML `xml:"bookmakers>bookmaker"`
}

type soccerTeamXML struct {
	Name string `xml:"name,attr"`
}

type soccerBookmakerXML struct {
	Name string          `xml:"name,attr"`
	Type []soccerTypeXML `xml:"type"`
}

type soccerTypeXML struct {
	Name string          `xml:"name,attr"`
	Odds []soccerOddXML  `xml:"odd"`
}

type soccerOddXML struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

func (c *Client) FetchSoccerOdds(ctx context.Context, league string) ([]PregameOdds, error) {
	url := c.oddsURL("soccer", league, "odds")

	var feed struct {
		Category soccerOddsXML `xml:"category"`
	}
	if err := c.fetchXML(ctx, url, &feed); err != nil {
		return nil, fmt.Errorf("fetch soccer odds %s: %w", league, err)
	}

	var results []PregameOdds
	preferred := []string{"Pinnacle", "Pinnacle Sports", "Bet365", "1xBet"}

	for _, m := range feed.Category.Matches {
		bm := pickSoccerBookmaker(m.Bookmaker, preferred)
		if bm == nil {
			continue
		}

		var homeOdd, drawOdd, awayOdd, overLine, underOdd float64
		for _, t := range bm.Type {
			switch strings.ToLower(t.Name) {
			case "1x2":
				for _, o := range t.Odds {
					v, err := strconv.ParseFloat(strings.TrimSpace(o.Value), 64)
					if err != nil {
						continue
					}
					switch strings.ToLower(o.Name) {
					case "1", "home":
						homeOdd = v
					case "x", "draw":
						drawOdd = v
					case "2", "away":
						awayOdd = v
					}
				}
			case "over/under", "ou":
				for _, o := range t.Odds {
					v, err := strconv.ParseFloat(strings.TrimSpace(o.Value), 64)
					if err != nil {
						continue
					}
					switch strings.ToLower(o.Name) {
					case "over":
						overLine = v
					case "under":
						underOdd = v
					}
				}
			}
		}

		if homeOdd <= 1 || drawOdd <= 1 || awayOdd <= 1 {
			continue
		}

		hp, dp, ap := threeWayToProb(homeOdd, drawOdd, awayOdd)
		g0 := estimateTotalGoals(overLine, underOdd)

		results = append(results, PregameOdds{
			HomeTeam:           m.HomeTeam.Name,
			AwayTeam:           m.AwayTeam.Name,
			GameID:             m.ID,
			GameTime:           m.Time,
			HomeWinPct:         hp,
			DrawPct:            dp,
			AwayWinPct:         ap,
			ExpectedTotalGoals: g0,
		})
	}

	return results, nil
}

// --- Probability helpers ---

// moneylineToProb converts American or decimal moneylines to vig-free probabilities.
func moneylineToProb(homeML, awayML float64) (float64, float64) {
	hImp := decimalToImplied(homeML)
	aImp := decimalToImplied(awayML)
	total := hImp + aImp
	if total <= 0 {
		return 0.5, 0.5
	}
	return hImp / total, aImp / total
}

func threeWayToProb(homeOdd, drawOdd, awayOdd float64) (float64, float64, float64) {
	hImp := 1.0 / homeOdd
	dImp := 1.0 / drawOdd
	aImp := 1.0 / awayOdd
	total := hImp + dImp + aImp
	if total <= 0 {
		return 1.0 / 3, 1.0 / 3, 1.0 / 3
	}
	return hImp / total, dImp / total, aImp / total
}

func decimalToImplied(odds float64) float64 {
	if odds <= 1.0 {
		return 0
	}
	return 1.0 / odds
}

// estimateTotalGoals derives expected goals from over/under odds.
// Uses Poisson CDF inversion: P(X > 2.5) = overImplied → solve for lambda.
func estimateTotalGoals(overOdd, underOdd float64) float64 {
	if overOdd <= 1.0 || underOdd <= 1.0 {
		return 2.5 // fallback
	}
	overImp := 1.0 / overOdd
	underImp := 1.0 / underOdd
	total := overImp + underImp
	pOver := overImp / total // P(goals > 2.5)

	// P(X <= 2) = 1 - pOver under Poisson with mean lambda.
	// Binary search for lambda.
	lo, hi := 0.5, 8.0
	for i := 0; i < 60; i++ {
		mid := (lo + hi) / 2.0
		cdf := poissonCDF(2, mid)
		if cdf > 1.0-pOver {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2.0
}

func poissonCDF(k int, lambda float64) float64 {
	sum := 0.0
	for i := 0; i <= k; i++ {
		sum += math.Exp(-lambda) * math.Pow(lambda, float64(i)) / factorial(i)
	}
	return sum
}

func factorial(n int) float64 {
	if n <= 1 {
		return 1
	}
	f := 1.0
	for i := 2; i <= n; i++ {
		f *= float64(i)
	}
	return f
}

func pickBookmaker(bms []hockeyBookmakerXML, preferred []string) *hockeyBookmakerXML {
	for _, pref := range preferred {
		for i := range bms {
			if strings.EqualFold(bms[i].Name, pref) {
				return &bms[i]
			}
		}
	}
	if len(bms) > 0 {
		return &bms[0]
	}
	return nil
}

func pickSoccerBookmaker(bms []soccerBookmakerXML, preferred []string) *soccerBookmakerXML {
	for _, pref := range preferred {
		for i := range bms {
			if strings.EqualFold(bms[i].Name, pref) {
				return &bms[i]
			}
		}
	}
	if len(bms) > 0 {
		return &bms[0]
	}
	return nil
}
