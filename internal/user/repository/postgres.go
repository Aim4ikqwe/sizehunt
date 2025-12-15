package repository

import (
	"context"
	"database/sql"
	"sizehunt/internal/user"
)

type PostgresUserRepository struct {
	db *sql.DB
}

func NewPostgresUserRepository(db *sql.DB) *PostgresUserRepository {
	return &PostgresUserRepository{db: db}
}

func (r *PostgresUserRepository) Create(ctx context.Context, u *user.User) error {
	query := `INSERT INTO users (email, password, created_at) VALUES ($1, $2, NOW()) RETURNING id`

	return r.db.QueryRowContext(ctx, query, u.Email, u.Password).Scan(&u.ID)
}

func (r *PostgresUserRepository) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	u := &user.User{}
	query := `SELECT id, email, password, created_at, is_admin FROM users WHERE email = $1`

	err := r.db.QueryRowContext(ctx, query, email).Scan(
		&u.ID,
		&u.Email,
		&u.Password,
		&u.CreatedAt,
		&u.IsAdmin,
	)
	if err != nil {
		return nil, err
	}

	return u, nil
}

func (r *PostgresUserRepository) GetByID(ctx context.Context, id int64) (*user.User, error) {
	u := &user.User{}
	query := `SELECT id, email, password, created_at, is_admin FROM users WHERE id = $1`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&u.ID,
		&u.Email,
		&u.Password,
		&u.CreatedAt,
		&u.IsAdmin,
	)
	if err != nil {
		return nil, err
	}

	return u, nil
}
