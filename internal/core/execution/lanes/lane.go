package lanes

// Lane encapsulates risk limits, throttle, and idempotency for a
// single (sport, league) execution path.
type Lane struct {
	risk       *RiskGuard
	throttle   *Throttle
	idempotent *IdempotencyGuard
}

func NewLane(maxOpenOrders int, maxOrderCents int, throttleMs int64) *Lane {
	return &Lane{
		risk:       NewRiskGuard(maxOpenOrders, maxOrderCents),
		throttle:   NewThrottle(throttleMs),
		idempotent: NewIdempotencyGuard(),
	}
}

// Allow returns true if an order for this ticker+score is permitted.
func (l *Lane) Allow(ticker string, homeScore, awayScore int) bool {
	key := l.idempotent.Key(ticker, homeScore, awayScore)

	if l.idempotent.HasSeen(key) {
		return false
	}

	if !l.risk.CanPlace() {
		return false
	}

	if !l.throttle.Allow() {
		return false
	}

	return true
}

// RecordOrder marks that an order was placed for this ticker+score combo.
func (l *Lane) RecordOrder(ticker string, homeScore, awayScore int) {
	key := l.idempotent.Key(ticker, homeScore, awayScore)
	l.idempotent.Record(key)
	l.risk.RecordPlacement()
	l.throttle.Touch()
}

// IdempotencyKey returns the dedup key for external use.
func (l *Lane) IdempotencyKey(ticker string, homeScore, awayScore int) string {
	return l.idempotent.Key(ticker, homeScore, awayScore)
}

// ClearIdempotency resets dedup state (e.g. after a score overturn).
func (l *Lane) ClearIdempotency() {
	l.idempotent.Clear()
}
