package entity

type Order struct {
	Price     float64
	Quantity  float64
	Side      string // BUY / SELL
	UpdatedAt int64
}
