package lanes

import "sync/atomic"

// RiskGuard tracks open order count and per-order size limits for a lane.
type RiskGuard struct {
	maxOpenOrders int
	maxOrderCents int
	openCount     atomic.Int32
}

func NewRiskGuard(maxOpenOrders, maxOrderCents int) *RiskGuard {
	return &RiskGuard{
		maxOpenOrders: maxOpenOrders,
		maxOrderCents: maxOrderCents,
	}
}

func (r *RiskGuard) CanPlace() bool {
	return int(r.openCount.Load()) < r.maxOpenOrders
}

func (r *RiskGuard) RecordPlacement() {
	r.openCount.Add(1)
}

func (r *RiskGuard) RecordFill() {
	r.openCount.Add(-1)
}

func (r *RiskGuard) RecordCancel() {
	r.openCount.Add(-1)
}

func (r *RiskGuard) MaxOrderCents() int {
	return r.maxOrderCents
}
