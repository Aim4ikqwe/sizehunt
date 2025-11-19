package user

import "time"

type User struct {
	ID        int       `json:"id"`
	Email     string    `json:"email"`
	Password  string    `json:"-"` // будем хранить только хэш
	CreatedAt time.Time `json:"created_at"`
}
