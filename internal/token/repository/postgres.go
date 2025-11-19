package repository

import (
	"context"
	"database/sql"
	"sizehunt/internal/token"
)

type RefreshTokenRepository struct {
	db *sql.DB
}

func NewRefreshTokenRepository(db *sql.DB) *RefreshTokenRepository {
	return &RefreshTokenRepository{db: db}
}

func (r *RefreshTokenRepository) Save(ctx context.Context, t *token.Token) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO refresh_tokens (user_id, token, expires_at) VALUES ($1, $2, $3)`,
		t.UserID, t.Token, t.ExpiresAt)
	return err
}

func (r *RefreshTokenRepository) GetByToken(ctx context.Context, tokenStr string) (*token.Token, error) {
	t := &token.Token{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, token, expires_at, created_at FROM refresh_tokens WHERE token = $1`,
		tokenStr).Scan(&t.ID, &t.UserID, &t.Token, &t.ExpiresAt, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (r *RefreshTokenRepository) DeleteByToken(ctx context.Context, tokenStr string) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE token = $1`,
		tokenStr)
	return err
}
