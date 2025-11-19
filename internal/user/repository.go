package user

type Repository interface {
	Create(u *User) error
	GetByEmail(email string) (*User, error)
}
