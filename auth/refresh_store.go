package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tvt/store"

	"github.com/CarlSinclair/csuit-libs/session"
)

type sessionStore struct{ st *store.Store }

func (s sessionStore) Alive(ctx context.Context, userID int64, sid string) (bool, error) {
	var n int
	err := s.st.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM refresh_tokens WHERE sid = ? AND user_id = ? AND expires_at > ?`,
		sid, userID, store.Now()).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s sessionStore) Revoke(ctx context.Context, userID int64, sid string) error {
	_, err := s.st.DB.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ? AND sid = ?`, userID, sid)
	return err
}

func (s sessionStore) RevokeAll(ctx context.Context, userID int64, except string) ([]string, error) {
	rows, err := s.st.DB.QueryContext(ctx, `SELECT DISTINCT sid FROM refresh_tokens WHERE user_id = ? AND sid <> ''`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var sids []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan session: %w", err)
		}
		if sid != except {
			sids = append(sids, sid)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close session list: %w", err)
	}
	if except == "" {
		_, err = s.st.DB.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ?`, userID)
	} else {
		_, err = s.st.DB.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE user_id = ? AND sid <> ?`, userID, except)
	}
	if err != nil {
		return nil, err
	}
	return sids, nil
}

type refreshStore struct{ st *store.Store }

func (r refreshStore) Find(ctx context.Context, hash string) (session.RefreshRecord, error) {
	var (
		rec          session.RefreshRecord
		expires      string
		supersededAt sql.NullString
		by           sql.NullString
	)
	err := r.st.DB.QueryRowContext(ctx,
		`SELECT user_id, sid, expires_at, superseded_at, superseded_by FROM refresh_tokens WHERE token_hash = ?`, hash,
	).Scan(&rec.UserID, &rec.SID, &expires, &supersededAt, &by)
	if errors.Is(err, sql.ErrNoRows) {
		return session.RefreshRecord{}, session.ErrRefreshNotFound
	}
	if err != nil {
		return session.RefreshRecord{}, err
	}
	rec.Expired = !store.ParseTs(expires).After(time.Now())
	rec.SupersededBy = by.String
	if supersededAt.Valid {
		at := store.ParseTs(supersededAt.String)
		if at.IsZero() {
			return session.RefreshRecord{}, fmt.Errorf("refresh token superseded_at has unreadable value %q", supersededAt.String)
		}
		rec.Superseded = true
		rec.SupersededAgo = max(time.Since(at), 0)
	}
	return rec, nil
}

func (r refreshStore) Rotate(ctx context.Context, _ session.RefreshRecord, oldHash, newHash string, userID int64, sid string, newExpiry time.Time) (bool, error) {
	tx, err := r.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE refresh_tokens SET superseded_at = ?, superseded_by = ? WHERE token_hash = ? AND superseded_at IS NULL`,
		store.Now(), newHash, oldHash)
	if err != nil {
		return false, fmt.Errorf("claim rotation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim rotation rows: %w", err)
	}
	if n != 1 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO refresh_tokens (user_id, sid, token_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, sid, newHash, store.Ts(newExpiry), store.Now()); err != nil {
		return false, fmt.Errorf("store successor: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM refresh_tokens WHERE user_id = ? AND (expires_at < ? OR (superseded_at IS NOT NULL AND superseded_at < ?))`,
		userID, store.Now(), store.Ts(time.Now().Add(-time.Hour))); err != nil {
		return false, fmt.Errorf("prune old tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit rotation: %w", err)
	}
	return true, nil
}
