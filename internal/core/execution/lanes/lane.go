package lanes

// Lane encapsulates spending limits and idempotency for a
// single (sport, league) execution path.
type Lane struct {
	maxGameCents int
	spend        *SpendGuard
	idempotent   *IdempotencyGuard
}

func NewLane(maxGameCents int, maxSportCents int) *Lane {
	return &Lane{
		maxGameCents: maxGameCents,
		spend:        NewSpendGuard(maxSportCents),
		idempotent:   NewIdempotencyGuard(),
	}
}

// NewLaneWithSpend creates a lane that shares an existing SpendGuard
// (so multiple leagues under the same sport share one sport-level cap).
func NewLaneWithSpend(maxGameCents int, spend *SpendGuard) *Lane {
	return &Lane{
		maxGameCents: maxGameCents,
		spend:        spend,
		idempotent:   NewIdempotencyGuard(),
	}
}

// MaxGameCents returns the per-game spending limit for this lane.
func (l *Lane) MaxGameCents() int {
	return l.maxGameCents
}

// Allow returns true if an order for this ticker+score is permitted.
func (l *Lane) Allow(ticker string, homeScore, awayScore int, orderCents int) bool {
	key := l.idempotent.Key(ticker, homeScore, awayScore)

	if l.idempotent.HasSeen(key) {
		return false
	}

	if !l.spend.CanSpend(orderCents) {
		return false
	}

	return true
}

// RecordOrder marks that an order was placed for this ticker+score combo
// and records the spending against the sport-level cap.
func (l *Lane) RecordOrder(ticker string, homeScore, awayScore int, orderCents int) {
	key := l.idempotent.Key(ticker, homeScore, awayScore)
	l.idempotent.Record(key)
	l.spend.Record(orderCents)
}

// IdempotencyKey returns the dedup key for external use.
func (l *Lane) IdempotencyKey(ticker string, homeScore, awayScore int) string {
	return l.idempotent.Key(ticker, homeScore, awayScore)
}

// ClearIdempotency resets dedup state (e.g. after a score overturn).
func (l *Lane) ClearIdempotency() {
	l.idempotent.Clear()
}
