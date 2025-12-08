package entity

// Order представляет ордер в стакане OKX
type Order struct {
	Price          float64
	Quantity       float64
	Side           string // buy / sell
	InstrumentType string // SPOT, MARGIN, SWAP, FUTURES, OPTION
	InstrumentID   string // например BTC-USDT-SWAP
	UpdatedAt      int64
}
