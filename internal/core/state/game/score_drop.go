package game

import "time"

type scoreDropRecord struct {
	firstSeen time.Time
	homeScore int
	awayScore int
}

// ScoreDropTracker detects and confirms spurious score decreases from
// the data feed. Embed this in any sport-specific GameState to get
// ClearScoreDropPending and IsScoreDropPending for free; the sport
// struct only needs a one-liner CheckScoreDrop wrapper that passes
// its current scores into CheckDrop.
type ScoreDropTracker struct {
	scoreDropPending bool
	scoreDropData    *scoreDropRecord
}

// CheckDrop is the core score-drop algorithm.
// curHome/curAway are the state's current scores; newHome/newAway are
// the incoming (potentially lower) scores from the feed.
func (t *ScoreDropTracker) CheckDrop(curHome, curAway, newHome, newAway, confirmSec int) string {
	prevTotal := curHome + curAway
	newTotal := newHome + newAway

	if newTotal >= prevTotal {
		if t.scoreDropPending {
			t.ClearScoreDropPending()
		}
		return "accept"
	}

	now := time.Now()
	if t.scoreDropData != nil {
		if newHome == t.scoreDropData.homeScore && newAway == t.scoreDropData.awayScore {
			if now.Sub(t.scoreDropData.firstSeen) >= time.Duration(confirmSec)*time.Second {
				t.ClearScoreDropPending()
				return "confirmed"
			}
		} else {
			t.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: newHome, awayScore: newAway}
		}
		t.scoreDropPending = true
		return "pending"
	}

	t.scoreDropData = &scoreDropRecord{firstSeen: now, homeScore: newHome, awayScore: newAway}
	t.scoreDropPending = true
	return "new_drop"
}

func (t *ScoreDropTracker) ClearScoreDropPending() {
	t.scoreDropPending = false
	t.scoreDropData = nil
}

func (t *ScoreDropTracker) IsScoreDropPending() bool {
	return t.scoreDropPending
}
