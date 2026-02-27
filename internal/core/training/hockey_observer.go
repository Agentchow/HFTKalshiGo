package training

import (
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	"github.com/charleschow/hft-trading/internal/core/ticker"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// HockeyObserver implements game.GameObserver. It writes training snapshots
// to the SQLite store on each game event and spawns a delayed backfill
// for LIVE Kalshi odds.
type HockeyObserver struct {
	store         *HockeyStore
	backfillDelay time.Duration
}

func NewHockeyObserver(store *HockeyStore, backfillDelaySec int) *HockeyObserver {
	return &HockeyObserver{
		store:         store,
		backfillDelay: time.Duration(backfillDelaySec) * time.Second,
	}
}

func (o *HockeyObserver) OnGameEvent(gc *game.GameContext, eventType string) {
	if eventType == "PRICE_UPDATE" {
		return
	}
	if o.store == nil || isMockGame(gc.EID) || gc.TotalVolume() < minTrainingVolume {
		return
	}
	hs, ok := gc.Game.(*hockeyState.HockeyState)
	if !ok {
		return
	}
	var outcome *string
	if eventType == string(events.StatusGameFinish) {
		o := hockeyOutcome(hs.HomeScore, hs.AwayScore)
		outcome = &o
	}

	row := buildHockeyRow(gc, hs, eventType, outcome)
	rowID, err := o.store.Insert(row)
	if err != nil {
		telemetry.Warnf("hockey training: insert failed: %v", err)
		return
	}
	o.spawnBackfill(gc, hs, rowID)
}

func buildHockeyRow(gc *game.GameContext, hs *hockeyState.HockeyState, eventType string, outcome *string) HockeyRow {
	row := HockeyRow{
		Ts:            time.Now(),
		GameID:        gc.EID,
		League:        gc.League,
		HomeTeam:      hs.HomeTeam,
		AwayTeam:      hs.AwayTeam,
		NormHome:      ticker.Normalize(hs.HomeTeam, ticker.HockeyAliases),
		NormAway:      ticker.Normalize(hs.AwayTeam, ticker.HockeyAliases),
		EventType:     eventType,
		HomeScore:     hs.HomeScore,
		AwayScore:     hs.AwayScore,
		Period:        hs.Period,
		TimeRemain:    hs.TimeLeft,
		HomePowerPlay: hs.IsHomePowerPlay,
		AwayPowerPlay: hs.IsAwayPowerPlay,
		ActualOutcome: outcome,
	}

	if hs.PregameApplied {
		row.PregameHomePct = f64Ptr(hs.HomeStrength)
		row.PregameAwayPct = f64Ptr(hs.AwayStrength)
		row.PregameG0 = hs.PregameG0
	}

	return row
}

func (o *HockeyObserver) spawnBackfill(gc *game.GameContext, hs *hockeyState.HockeyState, rowID int64) {
	delay := o.backfillDelay
	go func() {
		time.Sleep(delay)
		gc.Send(func() {
			odds := HockeyOddsBackfill{}

			if len(gc.Tickers) > 0 {
				if td, ok := gc.Tickers[hs.HomeTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiHomePctL = &v
				}
				if td, ok := gc.Tickers[hs.AwayTicker]; ok && td.YesAsk > 0 {
					v := td.YesAsk / 100.0
					odds.KalshiAwayPctL = &v
				}
			}

			o.store.BackfillOdds(rowID, odds)
		})
	}()
}

func hockeyOutcome(homeScore, awayScore int) string {
	if homeScore > awayScore {
		return "home_win"
	}
	if awayScore > homeScore {
		return "away_win"
	}
	return "shootout"
}
