package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SecretStatus is the redacted view of a stored secret safe to return to API/UI layers.
type SecretStatus struct {
	Key         string `json:"key"`
	Configured  bool   `json:"configured"`
	MaskedHint  string `json:"masked_hint"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

// SetSecret writes or updates a secret locally. Empty keys or values are rejected.
func (s *Store) SetSecret(ctx context.Context, key, val string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("store: secret key must not be empty")
	}
	if val == "" {
		return errors.New("store: secret value must not be empty (use DeleteSecret to remove)")
	}
	nowMs := time.Now().UnixMilli()
	_, err := s.writeDB.ExecContext(
		ctx,
		`INSERT INTO secrets (key, value, updated_at_ms) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		key, val, nowMs,
	)
	if err != nil {
		return fmt.Errorf("store: set secret %q: %w", key, err)
	}
	return nil
}

// DeleteSecret removes a stored secret by key.
func (s *Store) DeleteSecret(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("store: secret key must not be empty")
	}
	if _, err := s.writeDB.ExecContext(ctx, `DELETE FROM secrets WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete secret %q: %w", key, err)
	}
	return nil
}

// ListSecretStatus returns only redacted metadata for all stored secrets, never raw values.
func (s *Store) ListSecretStatus(ctx context.Context) ([]SecretStatus, error) {
	rows, err := s.readDB.QueryContext(
		ctx,
		`SELECT key, value, updated_at_ms FROM secrets ORDER BY key ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list secret status: %w", err)
	}
	defer rows.Close()

	var out []SecretStatus
	for rows.Next() {
		var key, val string
		var updatedAtMs int64
		if err := rows.Scan(&key, &val, &updatedAtMs); err != nil {
			return nil, fmt.Errorf("store: scan secret status: %w", err)
		}
		out = append(out, SecretStatus{
			Key:         key,
			Configured:  val != "",
			MaskedHint:  maskSecret(val),
			UpdatedAtMs: updatedAtMs,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate secret status: %w", err)
	}
	return out, nil
}

// ResolveSecret retrieves the raw secret value strictly for internal Kernel AI provider construction.
// Callers must never log or expose the returned value over API/UI boundaries.
func (s *Store) ResolveSecret(ctx context.Context, key string) (string, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false, errors.New("store: secret key must not be empty")
	}
	var val string
	err := s.readDB.QueryRowContext(ctx, `SELECT value FROM secrets WHERE key = ?`, key).Scan(&val)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: resolve secret %q: %w", key, err)
	}
	return val, val != "", nil
}

func maskSecret(val string) string {
	if val == "" {
		return ""
	}
	r := []rune(val)
	if len(r) <= 8 {
		return "***"
	}
	return "***" + string(r[len(r)-4:])
}
