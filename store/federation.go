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
	  WHERE f.status = 'accepted' AND (f.requester_id = ? OR f.addressee_id = ?) AND u.remote_host <> '' AND u.is_deactivated = 0`, userID, userID, userID)
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

type PeerInfo struct {
	Host      string
	FirstSeen string
	LastSeen  string
	Blocked   bool
	New       bool
	Share     bool
}

func (s *Store) CountPeers() (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM federation_peers`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count peers: %w", err)
	}
	return n, nil
}

func (s *Store) Peers(limit, offset int) ([]PeerInfo, error) {
	rows, err := s.DB.Query(`SELECT host, first_seen, last_seen, blocked, acked, share FROM federation_peers ORDER BY acked, host LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer rows.Close()
	var out []PeerInfo
	for rows.Next() {
		var p PeerInfo
		var b, ack, sh int
		if err := rows.Scan(&p.Host, &p.FirstSeen, &p.LastSeen, &b, &ack, &sh); err != nil {
			return nil, err
		}
		p.Blocked, p.New, p.Share = b == 1, ack == 0, sh == 1
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetPeerShare allow-lists a server: activity and episode comments are shared
// with it and accepted from it.
func (s *Store) SetPeerShare(host string, share bool) error {
	v := 0
	if share {
		v = 1
	}
	res, err := s.DB.Exec(`UPDATE federation_peers SET share = ? WHERE host = ?`, v, strings.ToLower(host))
	if err != nil {
		return fmt.Errorf("share peer %s: %w", host, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PeerShares says whether a server is allow-listed for sharing.
func (s *Store) PeerShares(host string) (bool, error) {
	var v int
	err := s.DB.QueryRow(`SELECT share FROM federation_peers WHERE host = ? AND blocked = 0`, strings.ToLower(host)).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("peer share %s: %w", host, err)
	}
	return v == 1, nil
}

// SharePeers lists the allow-listed servers.
func (s *Store) SharePeers() ([]string, error) {
	rows, err := s.DB.Query(`SELECT host FROM federation_peers WHERE share = 1 AND blocked = 0 ORDER BY host`)
	if err != nil {
		return nil, fmt.Errorf("list share peers: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Store) SetPeerBlocked(host string, blocked bool) error {
	v := 0
	if blocked {
		v = 1
	}
	res, err := s.DB.Exec(`UPDATE federation_peers SET blocked = ? WHERE host = ?`, v, strings.ToLower(host))
	if err != nil {
		return fmt.Errorf("block peer %s: %w", host, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ForgetPeer removes the pinned key, so the next contact learns it afresh.
func (s *Store) ForgetPeer(host string) error {
	if _, err := s.DB.Exec(`DELETE FROM federation_peers WHERE host = ?`, strings.ToLower(host)); err != nil {
		return fmt.Errorf("forget peer %s: %w", host, err)
	}
	return nil
}

// RememberSignature records a signature that was just accepted and reports
// whether it was new; a signature seen before is a replay. The record survives
// restarts.
func (s *Store) RememberSignature(sig string) (bool, error) {
	res, err := s.DB.Exec(`INSERT OR IGNORE INTO fed_seen (sig, seen_at) VALUES (?,?)`, sig, Now())
	if err != nil {
		return false, fmt.Errorf("remember signature: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) PurgeSignatures(olderThan time.Duration) error {
	_, err := s.DB.Exec(`DELETE FROM fed_seen WHERE seen_at < ?`, Ts(time.Now().Add(-olderThan)))
	if err != nil {
		return fmt.Errorf("purge signatures: %w", err)
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
	if _, err := s.DB.Exec(`DELETE FROM remote_comments WHERE expires_at < ?`, Now()); err != nil {
		return fmt.Errorf("purge remote comments: %w", err)
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

// CommentLease is how long a copy of another server's comment is kept unless
// that server confirms it still exists.
const CommentLease = 24 * time.Hour

// RemoteComment is a copy of a comment written on another server.
type RemoteComment struct {
	ID           int64
	UserID       int64
	Handle, Name string
	Host, Ref    string
	ParentRef    string
	// ParentHost is the server the parent comment lives on; empty means the
	// same server as this comment.
	ParentHost        string
	ShowTMDB          int64
	Season, Episode   int
	Body              string
	Spoiler           bool
	CreatedAt, Edited string
	ExpiresAt         string
}

func (s *Store) SaveRemoteComment(c RemoteComment) error {
	sp := 0
	if c.Spoiler {
		sp = 1
	}
	if _, err := s.DB.Exec(`INSERT INTO remote_comments (user_id, host, ref, parent_ref, parent_host, show_tmdb, season, episode, body, spoiler, created_at, edited_at, expires_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
	  ON CONFLICT(host, ref) DO UPDATE SET body = excluded.body, spoiler = excluded.spoiler, edited_at = excluded.edited_at, expires_at = excluded.expires_at`,
		c.UserID, strings.ToLower(c.Host), c.Ref, c.ParentRef, strings.ToLower(c.ParentHost), c.ShowTMDB, c.Season, c.Episode, c.Body, sp, c.CreatedAt, c.Edited, Ts(time.Now().Add(CommentLease))); err != nil {
		return fmt.Errorf("save remote comment %s/%s: %w", c.Host, c.Ref, err)
	}
	return nil
}

// RenewRemoteComment extends a lease and refreshes what the home server says
// the comment now reads.
func (s *Store) RenewRemoteComment(host, ref, body string, spoiler bool, edited string) error {
	sp := 0
	if spoiler {
		sp = 1
	}
	if _, err := s.DB.Exec(`UPDATE remote_comments SET body = ?, spoiler = ?, edited_at = ?, expires_at = ? WHERE host = ? AND ref = ?`,
		body, sp, edited, Ts(time.Now().Add(CommentLease)), strings.ToLower(host), ref); err != nil {
		return fmt.Errorf("renew remote comment %s/%s: %w", host, ref, err)
	}
	return nil
}

// DeleteRemoteComment removes a copy (and any replies to it from the same
// server) once its home server says it is gone.
func (s *Store) DeleteRemoteComment(host, ref string) error {
	if _, err := s.DB.Exec(`DELETE FROM remote_comments WHERE host = ? AND (ref = ? OR parent_ref = ?)`, strings.ToLower(host), ref, ref); err != nil {
		return fmt.Errorf("delete remote comment %s/%s: %w", host, ref, err)
	}
	return nil
}

// DeleteRemoteCommentsOf removes every copy of one person's comments.
func (s *Store) DeleteRemoteCommentsOf(userID int64) error {
	if _, err := s.DB.Exec(`DELETE FROM remote_comments WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete remote comments uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Store) RemoteCommentAuthor(host, ref string) (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT user_id FROM remote_comments WHERE host = ? AND ref = ?`, strings.ToLower(host), ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("remote comment author %s/%s: %w", host, ref, err)
	}
	return id, nil
}

// RemoteCommentsFor lists the copies of other servers' comments on one episode
// that the viewer may see: their authors are neither blocked nor suspended.
func (s *Store) RemoteCommentsFor(showTMDB int64, season, episode int, viewerID int64) ([]RemoteComment, error) {
	rows, err := s.DB.Query(`SELECT c.id, c.user_id, u.username, u.display_name, c.host, c.ref, c.parent_ref, c.parent_host, c.show_tmdb, c.season, c.episode, c.body, c.spoiler, c.created_at, c.edited_at, c.expires_at
	  FROM remote_comments c JOIN users u ON u.id = c.user_id
	  WHERE c.show_tmdb = ? AND c.season = ? AND c.episode = ? AND c.expires_at > ? AND u.is_deactivated = 0
	    AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ? AND b.blocked_id = c.user_id) OR (b.blocker_id = c.user_id AND b.blocked_id = ?))
	  ORDER BY c.created_at ASC, c.id ASC LIMIT 200`, showTMDB, season, episode, Now(), viewerID, viewerID)
	if err != nil {
		return nil, fmt.Errorf("remote comments show=%d s%de%d: %w", showTMDB, season, episode, err)
	}
	defer rows.Close()
	var out []RemoteComment
	for rows.Next() {
		var c RemoteComment
		var sp int
		if err := rows.Scan(&c.ID, &c.UserID, &c.Handle, &c.Name, &c.Host, &c.Ref, &c.ParentRef, &c.ParentHost, &c.ShowTMDB, &c.Season, &c.Episode, &c.Body, &sp, &c.CreatedAt, &c.Edited, &c.ExpiresAt); err != nil {
			return nil, err
		}
		c.Spoiler = sp == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RemoteCommentByID(id int64) (*RemoteComment, error) {
	var c RemoteComment
	var sp int
	err := s.DB.QueryRow(`SELECT c.id, c.user_id, u.username, u.display_name, c.host, c.ref, c.parent_ref, c.parent_host, c.show_tmdb, c.season, c.episode, c.body, c.spoiler, c.created_at, c.edited_at, c.expires_at
	  FROM remote_comments c JOIN users u ON u.id = c.user_id WHERE c.id = ?`, id).Scan(&c.ID, &c.UserID, &c.Handle, &c.Name, &c.Host, &c.Ref, &c.ParentRef, &c.ParentHost, &c.ShowTMDB, &c.Season, &c.Episode, &c.Body, &sp, &c.CreatedAt, &c.Edited, &c.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("remote comment %d: %w", id, err)
	}
	c.Spoiler = sp == 1
	return &c, nil
}

// DueRenewals lists copies whose lease ends within the window, newest first,
// so each can be re-confirmed with its home server.
func (s *Store) DueRenewals(within time.Duration, limit int) ([]RemoteComment, error) {
	rows, err := s.DB.Query(`SELECT id, user_id, '', '', host, ref, parent_ref, parent_host, show_tmdb, season, episode, body, spoiler, created_at, edited_at, expires_at
	  FROM remote_comments WHERE expires_at < ? ORDER BY host, expires_at LIMIT ?`, Ts(time.Now().Add(within)), limit)
	if err != nil {
		return nil, fmt.Errorf("due renewals: %w", err)
	}
	defer rows.Close()
	var out []RemoteComment
	for rows.Next() {
		var c RemoteComment
		var sp int
		if err := rows.Scan(&c.ID, &c.UserID, &c.Handle, &c.Name, &c.Host, &c.Ref, &c.ParentRef, &c.ParentHost, &c.ShowTMDB, &c.Season, &c.Episode, &c.Body, &sp, &c.CreatedAt, &c.Edited, &c.ExpiresAt); err != nil {
			return nil, err
		}
		c.Spoiler = sp == 1
		out = append(out, c)
	}
	return out, rows.Err()
}
