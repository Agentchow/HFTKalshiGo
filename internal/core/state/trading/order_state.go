package trading

// OrderState tracks open orders for a single game.
// NOT thread-safe on its own — protected by GameContext.Mu.
type OrderState struct {
	Open  map[string]*OpenOrder // orderID -> order
	Dedup map[string]bool       // dedupKey -> placed
}

type OpenOrder struct {
	OrderID string
	Ticker  string
	Side    string
	Count   int
	Price   int
	Status  string // "pending", "open", "filled", "cancelled"
}

func NewOrderState() *OrderState {
	return &OrderState{
		Open:  make(map[string]*OpenOrder),
		Dedup: make(map[string]bool),
	}
}

func (o *OrderState) TrackOrder(order *OpenOrder) {
	o.Open[order.OrderID] = order
}

func (o *OrderState) RemoveOrder(orderID string) {
	delete(o.Open, orderID)
}

func (o *OrderState) GetOrder(orderID string) (*OpenOrder, bool) {
	order, ok := o.Open[orderID]
	return order, ok
}

func (o *OrderState) OpenCount() int {
	return len(o.Open)
}

func (o *OrderState) HasDedup(key string) bool {
	return o.Dedup[key]
}

func (o *OrderState) SetDedup(key string) {
	o.Dedup[key] = true
}

// ClearDedup resets all dedup keys (e.g. after a score overturn).
func (o *OrderState) ClearDedup() {
	o.Dedup = make(map[string]bool)
}
