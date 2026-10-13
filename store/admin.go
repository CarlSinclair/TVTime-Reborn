package store

import (
	"fmt"
	"strings"
	"time"
)

type AdminStats struct {
	Users, NewUsers7d, Banned, Unverified int
	Shows, Follows, Watched, Comments     int
	OpenReports, Peers, UnseenPeers       int
	OutboxPending, RemoteEvents           int
}

func (s *Store) count(what, q string, args ...any) (int, error) {
	var n int
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("admin count %s: %w", what, err)
	}
	return n, nil
}

func (s *Store) OpenReportCount() (int, error) {
	a, err := s.count("comment reports", `SELECT COUNT(DISTINCT r.comment_id) FROM comment_reports r JOIN comments c ON c.id = r.comment_id
	  WHERE r.kind = 'abuse' AND r.resolved_at IS NULL AND c.deleted_at IS NULL AND c.hidden_at IS NULL`)
	if err != nil {
		return 0, err
	}
	b, err := s.count("user reports", `SELECT COUNT(DISTINCT reported_id) FROM user_reports WHERE resolved_at IS NULL`)
	if err != nil {
		return 0, err
	}
	return a + b, nil
}

func (s *Store) UnseenPeerCount() (int, error) {
	return s.count("unseen peers", `SELECT COUNT(*) FROM federation_peers WHERE acked = 0`)
}

// AckPeers marks every known server as seen by an admin, which clears the badge.
func (s *Store) AckPeers() error {
	if _, err := s.DB.Exec(`UPDATE federation_peers SET acked = 1 WHERE acked = 0`); err != nil {
		return fmt.Errorf("ack peers: %w", err)
	}
	return nil
}

func (s *Store) AckPeer(host string) error {
	if _, err := s.DB.Exec(`UPDATE federation_peers SET acked = 1 WHERE host = ?`, strings.ToLower(host)); err != nil {
		return fmt.Errorf("ack peer %s: %w", host, err)
	}
	return nil
}

func (s *Store) AdminStats() (AdminStats, error) {
	var st AdminStats
	var err error
	week := Ts(time.Now().AddDate(0, 0, -7))
	for _, c := range []struct {
		dst  *int
		what string
		q    string
		args []any
	}{
		{&st.Users, "users", `SELECT COUNT(*) FROM users WHERE remote_host = '' AND username <> 'deleted'`, nil},
		{&st.NewUsers7d, "new users", `SELECT COUNT(*) FROM users WHERE remote_host = '' AND created_at >= ?`, []any{week}},
		{&st.Banned, "banned", `SELECT COUNT(*) FROM users WHERE remote_host = '' AND is_banned = 1`, nil},
		{&st.Unverified, "unverified", `SELECT COUNT(*) FROM users WHERE remote_host = '' AND email_verified = 0 AND telegram_id IS NULL`, nil},
		{&st.Shows, "shows", `SELECT COUNT(*) FROM shows`, nil},
		{&st.Follows, "follows", `SELECT COUNT(*) FROM follows`, nil},
		{&st.Watched, "watched", `SELECT COUNT(*) FROM watched`, nil},
		{&st.Comments, "comments", `SELECT COUNT(*) FROM comments WHERE deleted_at IS NULL AND hidden_at IS NULL`, nil},
		{&st.Peers, "peers", `SELECT COUNT(*) FROM federation_peers`, nil},
		{&st.OutboxPending, "outbox", `SELECT COUNT(*) FROM fed_outbox`, nil},
		{&st.RemoteEvents, "remote events", `SELECT COUNT(*) FROM remote_events`, nil},
	} {
		if *c.dst, err = s.count(c.what, c.q, c.args...); err != nil {
			return st, err
		}
	}
	if st.OpenReports, err = s.OpenReportCount(); err != nil {
		return st, err
	}
	if st.UnseenPeers, err = s.UnseenPeerCount(); err != nil {
		return st, err
	}
	return st, nil
}

// AdminUsers lists the people who have an account on this server, newest
// first, optionally filtered by username, display name or email.
func (s *Store) AdminUsers(q string, limit, offset int) ([]User, int, error) {
	where := `remote_host = '' AND username <> 'deleted'`
	var args []any
	if q = strings.TrimSpace(q); q != "" {
		like := "%" + escapeLike(q) + "%"
		where += ` AND (username LIKE ? ESCAPE '\' OR display_name LIKE ? ESCAPE '\' OR COALESCE(email,'') LIKE ? ESCAPE '\')`
		args = append(args, like, like, like)
	}
	total, err := s.count("users list", `SELECT COUNT(*) FROM users WHERE `+where, args...)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.DB.Query(`SELECT `+userCols+` FROM users WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("admin list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("admin scan user: %w", err)
		}
		out = append(out, *u)
	}
	return out, total, rows.Err()
}

// AdminUpdateUser changes the fields an admin may edit on someone's account.
func (s *Store) AdminUpdateUser(id int64, displayName, email string, verified bool) error {
	v := 0
	if verified {
		v = 1
	}
	if _, err := s.DB.Exec(`UPDATE users SET display_name = ?, email = ?, email_verified = ?, pending_email = '' WHERE id = ?`,
		displayName, nullIfEmpty(email), v, id); err != nil {
		if isUnique(err, "users.email") {
			return ErrEmailTaken
		}
		return fmt.Errorf("admin update user %d: %w", id, err)
	}
	return nil
}

const mailLogKept = 200

// maskAddress hides most of an address so the log is useful for support
// without becoming a second list of everyone's email.
func maskAddress(e string) string {
	at := strings.LastIndex(e, "@")
	if at < 1 {
		return e
	}
	return e[:1] + "***" + e[at:]
}

// LogMail records the outcome of one send attempt and keeps only the latest.
func (s *Store) LogMail(to, subject string, sendErr error) error {
	ok, msg := 1, ""
	if sendErr != nil {
		ok, msg = 0, sendErr.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
	}
	if len(subject) > 200 {
		subject = subject[:200]
	}
	if _, err := s.DB.Exec(`INSERT INTO mail_log (recipient, subject, ok, error, created_at) VALUES (?,?,?,?,?)`, maskAddress(to), subject, ok, msg, Now()); err != nil {
		return fmt.Errorf("log mail: %w", err)
	}
	if _, err := s.DB.Exec(`DELETE FROM mail_log WHERE id <= (SELECT MAX(id) FROM mail_log) - ?`, mailLogKept); err != nil {
		return fmt.Errorf("prune mail log: %w", err)
	}
	return nil
}

type MailEntry struct {
	At, To, Subject, Error string
	OK                     bool
}

func (s *Store) RecentMail(limit int) ([]MailEntry, error) {
	rows, err := s.DB.Query(`SELECT created_at, recipient, subject, ok, error FROM mail_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent mail: %w", err)
	}
	defer rows.Close()
	var out []MailEntry
	for rows.Next() {
		var m MailEntry
		var ok int
		if err := rows.Scan(&m.At, &m.To, &m.Subject, &ok, &m.Error); err != nil {
			return nil, err
		}
		m.OK = ok == 1
		out = append(out, m)
	}
	return out, rows.Err()
}
