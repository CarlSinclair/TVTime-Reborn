package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Notification struct {
	ID        int64
	UserID    int64
	Event     string
	Category  string
	TitleKey  string
	BodyKey   string
	Data      map[string]any
	Link      string
	Read      bool
	CreatedAt string
}

func (s *Store) InsertNotification(n *Notification) (int64, error) {
	data, err := json.Marshal(n.Data)
	if err != nil {
		return 0, fmt.Errorf("marshal notification data uid=%d event=%s: %w", n.UserID, n.Event, err)
	}
	res, err := s.DB.Exec(`INSERT INTO notifications (user_id, event, category, title_key, body_key, data, link, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		n.UserID, n.Event, n.Category, n.TitleKey, n.BodyKey, string(data), n.Link, Now())
	if err != nil {
		return 0, fmt.Errorf("insert notification uid=%d event=%s: %w", n.UserID, n.Event, err)
	}
	return res.LastInsertId()
}

func scanNotification(row interface{ Scan(...any) error }) (Notification, error) {
	var n Notification
	var data string
	var read int
	if err := row.Scan(&n.ID, &n.UserID, &n.Event, &n.Category, &n.TitleKey, &n.BodyKey, &data, &n.Link, &read, &n.CreatedAt); err != nil {
		return n, err
	}
	n.Read = read == 1
	n.Data = map[string]any{}
	if data != "" {
		if err := json.Unmarshal([]byte(data), &n.Data); err != nil {
			return n, fmt.Errorf("decode notification %d data: %w", n.ID, err)
		}
	}
	return n, nil
}

const notifCols = `id, user_id, event, category, title_key, body_key, data, link, read, created_at`

func (s *Store) Notifications(userID int64, limit, offset int) ([]Notification, error) {
	rows, err := s.DB.Query(`SELECT `+notifCols+` FROM notifications WHERE user_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list notifications uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) NotificationByID(userID, id int64) (*Notification, error) {
	n, err := scanNotification(s.DB.QueryRow(`SELECT `+notifCols+` FROM notifications WHERE id = ? AND user_id = ?`, id, userID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("notification by id=%d uid=%d: %w", id, userID, err)
	}
	return &n, nil
}

func (s *Store) CountNotifications(userID int64) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count notifications uid=%d: %w", userID, err)
	}
	return n, nil
}

func (s *Store) UnreadCount(userID int64) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read = 0`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("unread count uid=%d: %w", userID, err)
	}
	return n, nil
}

func (s *Store) MarkNotificationRead(userID, id int64) error {
	res, err := s.DB.Exec(`UPDATE notifications SET read = 1 WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("mark notification read id=%d uid=%d: %w", id, userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkAllNotificationsRead(userID int64) error {
	if _, err := s.DB.Exec(`UPDATE notifications SET read = 1 WHERE user_id = ? AND read = 0`, userID); err != nil {
		return fmt.Errorf("mark all notifications read uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Store) PruneNotifications(olderThan time.Time) (int64, error) {
	res, err := s.DB.Exec(`DELETE FROM notifications WHERE created_at < ?`, Ts(olderThan))
	if err != nil {
		return 0, fmt.Errorf("prune notifications: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type ChannelPref struct{ InApp, Push, Email, Telegram bool }

func (s *Store) NotificationPrefs(userID int64) (map[string]ChannelPref, error) {
	rows, err := s.DB.Query(`SELECT category, in_app, push, email, telegram FROM notification_prefs WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("notification prefs uid=%d: %w", userID, err)
	}
	defer rows.Close()
	out := map[string]ChannelPref{}
	for rows.Next() {
		var cat string
		var a, p, e, t int
		if err := rows.Scan(&cat, &a, &p, &e, &t); err != nil {
			return nil, err
		}
		out[cat] = ChannelPref{a == 1, p == 1, e == 1, t == 1}
	}
	return out, rows.Err()
}

func (s *Store) SaveNotificationPrefs(userID int64, prefs map[string]ChannelPref) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin save prefs uid=%d: %w", userID, err)
	}
	defer tx.Rollback()
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	for cat, p := range prefs {
		if _, err := tx.Exec(`INSERT INTO notification_prefs (user_id, category, in_app, push, email, telegram) VALUES (?,?,?,?,?,?)
		  ON CONFLICT(user_id, category) DO UPDATE SET in_app=excluded.in_app, push=excluded.push, email=excluded.email, telegram=excluded.telegram`,
			userID, cat, b(p.InApp), b(p.Push), b(p.Email), b(p.Telegram)); err != nil {
			return fmt.Errorf("save pref uid=%d category=%s: %w", userID, cat, err)
		}
	}
	return tx.Commit()
}

type PushSub struct {
	ID        int64
	UserID    int64
	Endpoint  string
	P256dh    string
	Auth      string
	UserAgent string
}

func (s *Store) SavePushSub(userID int64, endpoint, p256dh, auth, ua string) error {
	if userID <= 0 || endpoint == "" || p256dh == "" || auth == "" {
		return errors.New("missing required subscription fields")
	}
	if _, err := s.DB.Exec(`INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth, user_agent, created_at, last_used_at) VALUES (?,?,?,?,?,?,?)
	  ON CONFLICT(endpoint) DO UPDATE SET user_id=excluded.user_id, p256dh=excluded.p256dh, auth=excluded.auth, user_agent=excluded.user_agent, last_used_at=excluded.last_used_at`,
		userID, endpoint, p256dh, auth, ua, Now(), Now()); err != nil {
		return fmt.Errorf("save push subscription uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Store) DeletePushSub(userID int64, endpoint string) error {
	if _, err := s.DB.Exec(`DELETE FROM push_subscriptions WHERE user_id = ? AND endpoint = ?`, userID, endpoint); err != nil {
		return fmt.Errorf("delete push subscription uid=%d: %w", userID, err)
	}
	return nil
}

func (s *Store) PushSubs(userID int64) ([]PushSub, error) {
	rows, err := s.DB.Query(`SELECT id, user_id, endpoint, p256dh, auth, user_agent FROM push_subscriptions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("list push subscriptions uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var p PushSub
		if err := rows.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.P256dh, &p.Auth, &p.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type EpisodeAlert struct {
	UserID    int64
	EpisodeID int64
	ShowID    int64
	ShowTMDB  int64
	ShowName  string
	Season    int
	Episode   int
	Name      string
	AirDate   string
	AirAt     string
}

// PendingEpisodeAlerts returns (user, episode) pairs for followed running shows
// whose episode aired in [from, to] and that the user has not been told about.
// The caller filters by each user's own local date.
func (s *Store) PendingEpisodeAlerts(from, to string) ([]EpisodeAlert, error) {
	rows, err := s.DB.Query(`SELECT f.user_id, e.id, sh.id, sh.tmdb_id, sh.name, e.season_number, e.episode_number, e.name, e.air_date, COALESCE(e.air_at, '')
	  FROM follows f
	  JOIN shows sh ON sh.id = f.show_id
	  JOIN episodes e ON e.show_id = sh.id
	  JOIN users u ON u.id = f.user_id AND u.is_deactivated = 0
	  WHERE f.status = 'watching' AND e.season_number > 0 AND e.air_date >= ? AND e.air_date <= ?
	    AND NOT EXISTS (SELECT 1 FROM episode_notified n WHERE n.user_id = f.user_id AND n.episode_id = e.id)
	    AND NOT EXISTS (SELECT 1 FROM watched w WHERE w.user_id = f.user_id AND w.episode_id = e.id)
	  ORDER BY e.air_date, e.season_number, e.episode_number`, from, to)
	if err != nil {
		return nil, fmt.Errorf("pending episode alerts: %w", err)
	}
	defer rows.Close()
	var out []EpisodeAlert
	for rows.Next() {
		var a EpisodeAlert
		if err := rows.Scan(&a.UserID, &a.EpisodeID, &a.ShowID, &a.ShowTMDB, &a.ShowName, &a.Season, &a.Episode, &a.Name, &a.AirDate, &a.AirAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) MarkEpisodeNotified(userID, episodeID int64) error {
	if _, err := s.DB.Exec(`INSERT INTO episode_notified (user_id, episode_id, created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`, userID, episodeID, Now()); err != nil {
		return fmt.Errorf("mark episode notified uid=%d ep=%d: %w", userID, episodeID, err)
	}
	return nil
}

func (s *Store) KVGet(key string) string { return s.SyncStateGet(key) }

func (s *Store) KVSet(key, value string) error { return s.SyncStateSet(key, value) }

func (s *Store) KVDelete(key string) error {
	if _, err := s.DB.Exec(`DELETE FROM sync_state WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete sync_state %s: %w", key, err)
	}
	return nil
}
