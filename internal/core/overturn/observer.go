package overturn

import (
	"time"

	"github.com/charleschow/hft-trading/internal/core/state/game"
	hockeyState "github.com/charleschow/hft-trading/internal/core/state/game/hockey"
	soccerState "github.com/charleschow/hft-trading/internal/core/state/game/soccer"
	"github.com/charleschow/hft-trading/internal/events"
	"github.com/charleschow/hft-trading/internal/telemetry"
)

// Observer implements game.GameObserver. It listens for overturn events
// and persists them with current Kalshi/Bet365 odds to the overturn DB.
type Observer struct {
	store *Store
}

func NewObserver(store *Store) *Observer {
	return &Observer{store: store}
}

func (o *Observer) OnGameEvent(gc *game.GameContext, eventType string) {
	if !isOverturnEvent(eventType) || o.store == nil || gc.LastOverturn == nil {
		return
	}

	ot := gc.LastOverturn
	row := Row{
		Ts:           time.Now(),
		Sport:        string(gc.Sport),
		GameID:       gc.EID,
		League:       gc.League,
		HomeTeam:     gc.Game.GetHomeTeam(),
		AwayTeam:     gc.Game.GetAwayTeam(),
		EventType:    eventType,
		OldHomeScore: ot.OldHome,
		OldAwayScore: ot.OldAway,
		NewHomeScore: ot.NewHome,
		NewAwayScore: ot.NewAway,
		Period:       gc.Game.GetPeriod(),
		TimeRemain:   gc.Game.GetTimeRemaining(),
	}

	switch ss := gc.Game.(type) {
	case *soccerState.SoccerState:
		row.KalshiHomeYes = tickerYesAsk(gc.Tickers, ss.HomeTicker)
		row.KalshiAwayYes = tickerYesAsk(gc.Tickers, ss.AwayTicker)
		row.KalshiDrawYes = tickerYesAsk(gc.Tickers, ss.DrawTicker)
		row.Bet365HomePct = pctPtr(ss.Bet365HomePct)
		row.Bet365AwayPct = pctPtr(ss.Bet365AwayPct)
		row.Bet365DrawPct = pctPtr(ss.Bet365DrawPct)
	case *hockeyState.HockeyState:
		row.KalshiHomeYes = tickerYesAsk(gc.Tickers, ss.HomeTicker)
		row.KalshiAwayYes = tickerYesAsk(gc.Tickers, ss.AwayTicker)
		row.Bet365HomePct = pctPtr(ss.Bet365HomePct)
		row.Bet365AwayPct = pctPtr(ss.Bet365AwayPct)
	}

	if err := o.store.Insert(row); err != nil {
		telemetry.Warnf("overturn store: insert failed: %v", err)
	}
}

func isOverturnEvent(eventType string) bool {
	return eventType == string(events.StatusOverturnPending) ||
		eventType == string(events.StatusOverturnConfirmed) ||
		eventType == string(events.StatusOverturnRejected)
}

func tickerYesAsk(tickers map[string]*game.TickerData, ticker string) *float64 {
	if ticker == "" {
		return nil
	}
	td, ok := tickers[ticker]
	if !ok || td.YesAsk <= 0 {
		return nil
	}
	v := td.YesAsk
	return &v
}

func pctPtr(p *float64) *float64 {
	if p == nil || *p <= 0 {
		return nil
	}
	v := *p
	return &v
}
