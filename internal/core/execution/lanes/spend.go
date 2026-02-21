package lanes

import "sync/atomic"

// SpendGuard tracks total cents spent across all games in a lane
// and enforces a per-sport spending cap.
type SpendGuard struct {
	maxCents  int64
	spentCents atomic.Int64
}

func NewSpendGuard(maxCents int) *SpendGuard {
	return &SpendGuard{maxCents: int64(maxCents)}
}

func (s *SpendGuard) CanSpend(cents int) bool {
	return s.spentCents.Load()+int64(cents) <= s.maxCents
}

func (s *SpendGuard) Record(cents int) {
	s.spentCents.Add(int64(cents))
}

func (s *SpendGuard) TotalSpent() int {
	return int(s.spentCents.Load())
}
