package goalserve

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// InplayOdds represents live in-play odds for a single match.
type InplayOdds struct {
	MatchID  string
	HomeTeam string
	AwayTeam string

	HomeWinPct float64 // vig-free, 0–1
	DrawPct    float64 // vig-free, 0–1
	AwayWinPct float64 // vig-free, 0–1

	HomeScore int
	AwayScore int
	Timer     string // e.g. "34:12"
	Half      string
	Bookmaker string // source book for these odds (if reported by feed)
}

type inplayFeedXML struct {
	Matches []inplayMatchXML `xml:"match"`
}

type inplayMatchXML struct {
	ID        string              `xml:"id,attr"`
	HomeTeam  string              `xml:"hometeam,attr"`
	AwayTeam  string              `xml:"awayteam,attr"`
	HomeScore string              `xml:"home_score,attr"`
	AwayScore string              `xml:"away_score,attr"`
	Timer     string              `xml:"timer,attr"`
	Half      string              `xml:"half,attr"`
	Odds      []inplayOddXML      `xml:"odds>odd"`
	Bookmaker []inplayBookXML     `xml:"odds"`
}

type inplayBookXML struct {
	Name string `xml:"bookmaker,attr"`
}

type inplayOddXML struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// FetchInplayOdds returns live vig-free 1X2 probabilities from
// GoalServe's inplay feed. Used for training data collection.
func (c *Client) FetchInplayOdds(ctx context.Context, sport string) ([]InplayOdds, error) {
	url := c.inplayURL(sport)

	var feed struct {
		Category inplayFeedXML `xml:"category"`
	}
	if err := c.fetchXML(ctx, url, &feed); err != nil {
		return nil, fmt.Errorf("fetch inplay %s: %w", sport, err)
	}

	var results []InplayOdds
	for _, m := range feed.Category.Matches {
		var homeOdd, drawOdd, awayOdd float64
		for _, o := range m.Odds {
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
		if homeOdd <= 1 || awayOdd <= 1 {
			continue
		}

		hp, dp, ap := threeWayToProb(homeOdd, drawOdd, awayOdd)
		hs, _ := strconv.Atoi(strings.TrimSpace(m.HomeScore))
		as, _ := strconv.Atoi(strings.TrimSpace(m.AwayScore))

		var bookName string
		if len(m.Bookmaker) > 0 {
			bookName = m.Bookmaker[0].Name
		}

		results = append(results, InplayOdds{
			MatchID:    m.ID,
			HomeTeam:   m.HomeTeam,
			AwayTeam:   m.AwayTeam,
			HomeWinPct: hp,
			DrawPct:    dp,
			AwayWinPct: ap,
			HomeScore:  hs,
			AwayScore:  as,
			Timer:      m.Timer,
			Half:       m.Half,
			Bookmaker:  bookName,
		})
	}

	return results, nil
}
