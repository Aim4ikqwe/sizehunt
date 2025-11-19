package service

import (
	"context"
	"errors"

	"golang.org/x/crypto/bcrypt"
	"sizehunt/internal/user"
)

var (
	ErrUserExists   = errors.New("user already exists")
	ErrInvalidCreds = errors.New("invalid credentials")
)

type UserRepository interface {
	Create(context.Context, *user.User) error
	GetByEmail(context.Context, string) (*user.User, error)
}

type UserService struct {
	repo UserRepository
}

func NewUserService(repo UserRepository) *UserService {
	return &UserService{repo: repo}
}

func (s *UserService) Register(ctx context.Context, email, password string) (*user.User, error) {
	_, err := s.repo.GetByEmail(ctx, email)
	if err == nil {
		return nil, ErrUserExists
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)

	u := &user.User{
		Email:    email,
		Password: string(hash),
	}

	err = s.repo.Create(ctx, u)
	if err != nil {
		return nil, err
	}

	return u, nil
}

func (s *UserService) Login(ctx context.Context, email, password string) (*user.User, error) {
	u, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		return nil, ErrInvalidCreds
	}

	err = bcrypt.CompareHashAndPassword([]byte(u.Password), []byte(password))
	if err != nil {
		return nil, ErrInvalidCreds
	}

	return u, nil
}
