package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const EventLease = 14 * 24 * time.Hour

func (u *User) IsRemote() bool { return u.RemoteHost != "" }

// RemoteHandle is the name@host form of a person on another server.
func RemoteHandle(name, host string) string {
	return strings.ToLower(name) + "@" + strings.ToLower(host)
}

// LocalPart is the username on its own server.
func (u *User) LocalPart() string {
	if u.IsRemote() {
		if i := strings.LastIndex(u.Username, "@"); i > 0 {
			return u.Username[:i]
		}
	}
	return u.Username
}

// EnsureRemoteUser creates or refreshes the local stand-in for a person on
// another server. It has no password or email and can never sign in; it exists
// so friendships, blocks, reports and the feed work with one kind of user.
func (s *Store) EnsureRemoteUser(host, name, display string) (*User, error) {
	handle := RemoteHandle(name, host)
	u, err := s.UserByUsername(handle)
	if err == nil {
		if u.RemoteHost == "" {
			return nil, ErrForbidden
		}
		if display != "" && display != u.DisplayName {
			if _, err := s.DB.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, display, u.ID); err != nil {
				return nil, fmt.Errorf("refresh remote user %s: %w", handle, err)
			}
			u.DisplayName = display
		}
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if _, err := s.DB.Exec(`INSERT INTO users (username, display_name, remote_host, locale, notify_in_app, notify_push, notify_email, notify_telegram, created_at) VALUES (?,?,?,'en',0,0,0,0,?)`,
		handle, display, strings.ToLower(host), Now()); err != nil {
		return nil, fmt.Errorf("create remote user %s: %w", handle, err)
	}
	return s.UserByUsername(handle)
}

type RemoteFriend struct {
	UserID   int64
	Username string
	Host     string
}

// RemoteFriends are the accepted friends of a local user who live elsewhere.
func (s *Store) RemoteFriends(userID int64) ([]RemoteFriend, error) {
	rows, err := s.DB.Query(`SELECT u.id, u.username, u.remote_host FROM friend_requests f
	  JOIN users u ON u.id = CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END
	  WHERE f.status = 'accepted' AND (f.requester_id = ? OR f.addressee_id = ?) AND u.remote_host <> '' AND u.is_banned = 0`, userID, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("remote friends uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []RemoteFriend
	for rows.Next() {
		var f RemoteFriend
		if err := rows.Scan(&f.UserID, &f.Username, &f.Host); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// HasLocalFriendOf says whether anyone on this server is friends with the
// given remote person, which is what entitles that person's activity to be kept.
func (s *Store) HasLocalFriendOf(remoteID int64) (bool, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM friend_requests f JOIN users u ON u.id = CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END
	  WHERE f.status = 'accepted' AND (f.requester_id = ? OR f.addressee_id = ?) AND u.remote_host = ''`, remoteID, remoteID, remoteID).Scan(&n); err != nil {
		return false, fmt.Errorf("local friends of %d: %w", remoteID, err)
	}
	return n > 0, nil
}

type Peer struct {
	Host      string
	PublicKey string
	Blocked   bool
}

func (s *Store) PeerByHost(host string) (*Peer, error) {
	var p Peer
	var b int
	err := s.DB.QueryRow(`SELECT host, public_key, blocked FROM federation_peers WHERE host = ?`, strings.ToLower(host)).Scan(&p.Host, &p.PublicKey, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("peer %s: %w", host, err)
	}
	p.Blocked = b == 1
	return &p, nil
}

func (s *Store) PinPeer(host, key string) error {
	if _, err := s.DB.Exec(`INSERT INTO federation_peers (host, public_key, first_seen, last_seen) VALUES (?,?,?,?)
	  ON CONFLICT(host) DO UPDATE SET last_seen = excluded.last_seen`, strings.ToLower(host), key, Now(), Now()); err != nil {
		return fmt.Errorf("pin peer %s: %w", host, err)
	}
	return nil
}

type OutboxItem struct {
	ID       int64
	Host     string
	Path     string
	Body     string
	Attempts int
}

func (s *Store) Enqueue(host, path, body string) error {
	if _, err := s.DB.Exec(`INSERT INTO fed_outbox (host, path, body, next_at, created_at) VALUES (?,?,?,?,?)`, strings.ToLower(host), path, body, Now(), Now()); err != nil {
		return fmt.Errorf("enqueue %s: %w", host, err)
	}
	return nil
}

func (s *Store) DueOutbox(limit int) ([]OutboxItem, error) {
	rows, err := s.DB.Query(`SELECT id, host, path, body, attempts FROM fed_outbox WHERE next_at <= ? ORDER BY id LIMIT ?`, Now(), limit)
	if err != nil {
		return nil, fmt.Errorf("due outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		if err := rows.Scan(&it.ID, &it.Host, &it.Path, &it.Body, &it.Attempts); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) OutboxDone(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM fed_outbox WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("outbox done %d: %w", id, err)
	}
	return nil
}

func (s *Store) OutboxRetry(id int64, attempts int, after time.Duration) error {
	_, err := s.DB.Exec(`UPDATE fed_outbox SET attempts = ?, next_at = ? WHERE id = ?`, attempts, Ts(time.Now().Add(after)), id)
	if err != nil {
		return fmt.Errorf("outbox retry %d: %w", id, err)
	}
	return nil
}

type RemoteEvent struct {
	Ref, Kind, Show, Poster, Excerpt, At string
	ShowTMDB                             int64
	Season, Episode, Count, Stars        int
}

func (s *Store) SaveRemoteEvent(userID int64, e RemoteEvent) error {
	if e.Count < 1 {
		e.Count = 1
	}
	if _, err := s.DB.Exec(`INSERT INTO remote_events (user_id, ref, kind, show_tmdb, show_name, poster, season, episode, count, stars, excerpt, at, expires_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(user_id, ref) DO UPDATE SET kind = excluded.kind, show_name = excluded.show_name, count = excluded.count, stars = excluded.stars, excerpt = excluded.excerpt, at = excluded.at, expires_at = excluded.expires_at`,
		userID, e.Ref, e.Kind, e.ShowTMDB, e.Show, e.Poster, e.Season, e.Episode, e.Count, e.Stars, e.Excerpt, e.At, Ts(time.Now().Add(EventLease))); err != nil {
		return fmt.Errorf("save remote event uid=%d ref=%s: %w", userID, e.Ref, err)
	}
	return nil
}

func (s *Store) RetractRemoteEvent(userID int64, ref string) error {
	_, err := s.DB.Exec(`DELETE FROM remote_events WHERE user_id = ? AND ref = ?`, userID, ref)
	if err != nil {
		return fmt.Errorf("retract remote event uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Store) PurgeExpiredRemote() error {
	if _, err := s.DB.Exec(`DELETE FROM remote_events WHERE expires_at < ?`, Now()); err != nil {
		return fmt.Errorf("purge remote events: %w", err)
	}
	return nil
}

func (s *Store) DropRemoteEvents(userID int64) error {
	_, err := s.DB.Exec(`DELETE FROM remote_events WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("drop remote events uid=%d: %w", userID, err)
	}
	return nil
}
