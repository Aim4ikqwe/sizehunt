package repository

import (
	"context"
	"database/sql"
	"sizehunt/internal/promocode"
)

type PostgresPromoCodeRepository struct {
	db *sql.DB
}

func NewPostgresPromoCodeRepository(db *sql.DB) *PostgresPromoCodeRepository {
	return &PostgresPromoCodeRepository{db: db}
}

func (r *PostgresPromoCodeRepository) Create(ctx context.Context, pc *promocode.PromoCode) error {
	query := `
        INSERT INTO promo_codes (code, days_duration, max_uses, is_active, created_by, expires_at) 
        VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`

	return r.db.QueryRowContext(ctx, query, pc.Code, pc.DaysDuration, pc.MaxUses, pc.IsActive, pc.CreatedBy, pc.ExpiresAt).Scan(&pc.ID)
}

func (r *PostgresPromoCodeRepository) GetByCode(ctx context.Context, code string) (*promocode.PromoCode, error) {
	pc := &promocode.PromoCode{}
	query := `SELECT id, code, days_duration, max_uses, used_count, is_active, created_by, created_at, expires_at 
              FROM promo_codes WHERE code = $1 AND is_active = true`

	err := r.db.QueryRowContext(ctx, query, code).Scan(
		&pc.ID,
		&pc.Code,
		&pc.DaysDuration,
		&pc.MaxUses,
		&pc.UsedCount,
		&pc.IsActive,
		&pc.CreatedBy,
		&pc.CreatedAt,
		&pc.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}

	return pc, nil
}

func (r *PostgresPromoCodeRepository) GetByID(ctx context.Context, id int64) (*promocode.PromoCode, error) {
	pc := &promocode.PromoCode{}
	query := `SELECT id, code, days_duration, max_uses, used_count, is_active, created_by, created_at, expires_at 
              FROM promo_codes WHERE id = $1`

	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&pc.ID,
		&pc.Code,
		&pc.DaysDuration,
		&pc.MaxUses,
		&pc.UsedCount,
		&pc.IsActive,
		&pc.CreatedBy,
		&pc.CreatedAt,
		&pc.ExpiresAt,
	)
	if err != nil {
		return nil, err
	}

	return pc, nil
}

func (r *PostgresPromoCodeRepository) IncrementUsage(ctx context.Context, promoCodeID int64) error {
	query := `UPDATE promo_codes SET used_count = used_count + 1 WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, promoCodeID)
	return err
}

func (r *PostgresPromoCodeRepository) RecordUsage(ctx context.Context, userID, promoCodeID int64) error {
	query := `INSERT INTO promo_code_usages (user_id, promo_code_id, used_at) VALUES ($1, $2, NOW())`
	_, err := r.db.ExecContext(ctx, query, userID, promoCodeID)
	return err
}

func (r *PostgresPromoCodeRepository) HasUserUsed(ctx context.Context, userID, promoCodeID int64) (bool, error) {
	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM promo_code_usages WHERE user_id = $1 AND promo_code_id = $2)`
	err := r.db.QueryRowContext(ctx, query, userID, promoCodeID).Scan(&exists)
	return exists, err
}

func (r *PostgresPromoCodeRepository) GetAll(ctx context.Context) ([]*promocode.PromoCode, error) {
	rows, err := r.db.QueryContext(ctx, `
        SELECT id, code, days_duration, max_uses, used_count, is_active, created_by, created_at, expires_at 
        FROM promo_codes ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var promoCodes []*promocode.PromoCode
	for rows.Next() {
		pc := &promocode.PromoCode{}
		err := rows.Scan(
			&pc.ID,
			&pc.Code,
			&pc.DaysDuration,
			&pc.MaxUses,
			&pc.UsedCount,
			&pc.IsActive,
			&pc.CreatedBy,
			&pc.CreatedAt,
			&pc.ExpiresAt,
		)
		if err != nil {
			return nil, err
		}
		promoCodes = append(promoCodes, pc)
	}

	return promoCodes, nil
}

func (r *PostgresPromoCodeRepository) Delete(ctx context.Context, id int64) error {
	query := `DELETE FROM promo_codes WHERE id = $1`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *PostgresPromoCodeRepository) Update(ctx context.Context, pc *promocode.PromoCode) error {
	query := `UPDATE promo_codes SET 
              code = $1, 
              days_duration = $2, 
              max_uses = $3, 
              is_active = $4, 
              expires_at = $5 
              WHERE id = $6`
	_, err := r.db.ExecContext(ctx, query,
		pc.Code,
		pc.DaysDuration,
		pc.MaxUses,
		pc.IsActive,
		pc.ExpiresAt,
		pc.ID)
	return err
}
