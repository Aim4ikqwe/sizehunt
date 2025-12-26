package entity

// Order представляет ордер в стакане Bybit
type Order struct {
	Price    float64
	Quantity float64
	Side     string // buy / sell
	Category string // spot, linear, inverse
	Symbol   string // например BTCUSDT
}
