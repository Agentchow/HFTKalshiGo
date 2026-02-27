package training

import (
	"strings"
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

const minTrainingVolume int64 = 20_000

// SoccerObserver implements game.GameObserver. It writes training snapshots
// to the SQLite store on each game event and spawns a delayed backfill
// for LIVE Kalshi odds.
type SoccerObserver struct {
	store         *Store
	backfillDelay time.Duration
}

func NewSoccerObserver(store *Store, backfillDelaySec int) *SoccerObserver {
	return &SoccerObserver{
		store:         store,
		backfillDelay: time.Duration(backfillDelaySec) * time.Second,
	}
}

func (o *SoccerObserver) OnGameEvent(gc *game.GameContext, eventType string) {
	if eventType == "PRICE_UPDATE" {
		return
	}
	if o.store == nil || isMockGame(gc.EID) || gc.TotalVolume() < minTrainingVolume {
		return
	}
	ss, ok := gc.Game.(*soccerState.SoccerState)
	if !ok {
		return
	}
	var outcome *string
	if eventType == string(events.StatusGameFinish) {
		o := regulationOutcome(ss)
		outcome = &o
	}

	row := buildSoccerRow(gc, ss, eventType, outcome)
	rowID, err := o.store.Insert(row)
	if err != nil {
		telemetry.Warnf("soccer training: insert failed: %v", err)
		return
	}
	o.spawnBackfill(gc, ss, rowID)
}

func buildSoccerRow(gc *game.GameContext, ss *soccerState.SoccerState, eventType string, outcome *string) SoccerRow {
	row := SoccerRow{
		Ts:            time.Now(),
		GameID:        gc.EID,
		League:        gc.League,
		HomeTeam:      ss.HomeTeam,
		AwayTeam:      ss.AwayTeam,
		NormHome:      ticker.Normalize(ss.HomeTeam, ticker.SoccerAliases),
		NormAway:      ticker.Normalize(ss.AwayTeam, ticker.SoccerAliases),
		Half:          ss.Half,
		EventType:     eventType,
		HomeScore:     ss.HomeScore,
		AwayScore:     ss.AwayScore,
		TimeRemain:    ss.TimeLeft,
		RedCardsHome:  ss.HomeRedCards,
		RedCardsAway:  ss.AwayRedCards,
		ActualOutcome: outcome,
	}

	if ss.PregameApplied {
		row.PregameHomePct = f64Ptr(ss.HomeStrength)
		row.PregameDrawPct = f64Ptr(ss.DrawPct)
		row.PregameAwayPct = f64Ptr(ss.AwayStrength)
		row.PregameG0 = f64Ptr(ss.G0)
	}

	return row
}

func (o *SoccerObserver) spawnBackfill(gc *game.GameContext, ss *soccerState.SoccerState, rowID int64) {
	delay := o.backfillDelay
	go func() {
		time.Sleep(delay)
		gc.Send(func() {
			odds := OddsBackfill{}

			if len(gc.Tickers) > 0 {
				if td, ok := gc.Tickers[ss.HomeTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiHomePctL = &v
				}
				if td, ok := gc.Tickers[ss.DrawTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiDrawPctL = &v
				}
				if td, ok := gc.Tickers[ss.AwayTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiAwayPctL = &v
				}
			}

			o.store.BackfillOdds(rowID, odds)
		})
	}()
}

func regulationOutcome(ss *soccerState.SoccerState) string {
	diff := ss.RegulationGoalDiff()
	if diff > 0 {
		return "home_win"
	}
	if diff < 0 {
		return "away_win"
	}
	return "draw"
}

func f64Ptr(v float64) *float64 { return &v }

func isMockGame(eid string) bool { return strings.HasPrefix(eid, "MOCK-") }
