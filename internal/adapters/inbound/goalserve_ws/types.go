package goalserve_ws

import "encoding/json"

// WSMessage is the top-level envelope; only the "mt" field is decoded first
// to route to the correct type (AvlMessage or UpdtMessage).
type WSMessage struct {
	MT string `json:"mt"` // "avl" or "updt"
}

// AvlMessage lists all currently LIVE events for a sport.
type AvlMessage struct {
	MT    string     `json:"mt"` // "avl"
	Sport string     `json:"sp"`
	DT    string     `json:"dt"`
	Evts  []AvlEvent `json:"evts"`
	BM    string     `json:"bm"`
}

type AvlEvent struct {
	ID      string      `json:"id"`
	MID     json.Number `json:"mid"`
	CmpID   json.Number `json:"cmp_id"`
	CmpName string      `json:"cmp_name"`
	T1      WSTeam      `json:"t1"`
	T2      WSTeam      `json:"t2"`
	PC      int         `json:"pc"`
	FI      string      `json:"fi"`
}

// UpdtMessage is a LIVE update for a single event/match.
type UpdtMessage struct {
	MT      string                     `json:"mt"` // "updt"
	Sport   string                     `json:"sp"`
	CtryID  json.Number                `json:"ctry_id"`
	BM      string                     `json:"bm"`
	ST      json.Number                `json:"st"`   // start time (unix), may be null
	Uptd    string                     `json:"uptd"` // "24.02.2026 18:42:20"
	PT      string                     `json:"pt"`   // push time (millis string)
	ID      string                     `json:"id"`
	MID     json.Number                `json:"mid"`
	CmpID   json.Number                `json:"cmp_id"`
	CmpName string                     `json:"cmp_name"`
	T1      WSTeam                     `json:"t1"`
	T2      WSTeam                     `json:"t2"`
	ET      int                        `json:"et"`  // elapsed time in seconds
	STP     int                        `json:"stp"` // 1 = time stopped, 0 = running
	BL      int                        `json:"bl"`  // 1 = event blocked
	XY      *string                    `json:"xy"`  // ball position "x,y" or null
	PC      int                        `json:"pc"`  // period code
	SC      string                     `json:"sc"`  // state code
	CMS     []WSComment                `json:"cms"` // play-by-play comments
	Stat    string                     `json:"stat"`
	Stats   map[string]json.RawMessage `json:"stats"`
	Odds    []WSOddsMarket             `json:"odds"`
}

type WSTeam struct {
	Name string `json:"n"`
	Kit  *WSKit `json:"kit,omitempty"`
}

type WSKit struct {
	ID int     `json:"id"`
	SI string  `json:"si"`
	SO *string `json:"so"`
}

type WSComment struct {
	ID string `json:"id"`
	MT string `json:"mt"` // message type code: "255"=goal, "125"=PP start, "129"=PP over, "128"=4on4
	P  string `json:"p"`  // period
	TM int    `json:"tm"` // time in seconds
	N  string `json:"n"`  // text
	TI string `json:"ti"` // team indicator: "0"=neutral, "1"=home, "2"=away
}

type WSOddsMarket struct {
	ID int           `json:"id"`
	BL int           `json:"bl"`           // blocked
	HA *float64      `json:"ha,omitempty"` // handicap value, nil = no handicap
	O  []WSOddsEntry `json:"o"`
}

type WSOddsEntry struct {
	Name    string  `json:"n"`
	Value   float64 `json:"v"`
	LastVal float64 `json:"lv,omitempty"`
	Blocked int     `json:"b"`
}
