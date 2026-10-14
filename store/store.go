package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"tvt/db"
)

type Store struct {
	DB *sql.DB
}

func New(d *sql.DB) *Store { return &Store{DB: d} }

var (
	ErrNotFound      = errors.New("not found")
	ErrEmailTaken    = errors.New("email already registered")
	ErrUsernameTaken = errors.New("username already taken")
)

func Now() string { return time.Now().UTC().Format(db.TimeFormat) }

func Ts(t time.Time) string { return t.UTC().Format(db.TimeFormat) }

func ParseTs(v string) time.Time {
	t, err := time.ParseInLocation(db.TimeFormat, v, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

func isUnique(err error, col string) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") && strings.Contains(s, col)
}
