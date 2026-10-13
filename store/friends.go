package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrAlreadyFriends = errors.New("already friends")
	ErrFriendBlocked  = errors.New("blocked")
	ErrSelfFriend     = errors.New("cannot befriend yourself")
)

type Person struct {
	ID          int64
	Username    string
	DisplayName string
	AvatarPath  string
}

func (p Person) Name() string {
	if strings.TrimSpace(p.DisplayName) != "" {
		return p.DisplayName
	}
	return p.Username
}

// FriendState is how the viewer stands with another person.
func (s *Store) FriendState(viewerID, otherID int64) (string, error) {
	var requester int64
	var status string
	err := s.DB.QueryRow(`SELECT requester_id, status FROM friend_requests
	  WHERE (requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`, viewerID, otherID, otherID, viewerID).Scan(&requester, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "none", nil
		}
		return "", fmt.Errorf("friend state %d-%d: %w", viewerID, otherID, err)
	}
	switch {
	case status == "accepted":
		return "friends", nil
	case requester == viewerID:
		return "outgoing", nil
	}
	return "incoming", nil
}

// RequestFriend sends a request and says whether it was new. Only one request
// from one person to another can wait at a time: asking again while it is
// unanswered changes nothing (created is false), and once it is declined a new
// one may be sent. When the other person already asked you, the two requests
// meet and become a friendship at once.
func (s *Store) RequestFriend(fromID, toID int64) (state string, created bool, err error) {
	if fromID == toID {
		return "", false, ErrSelfFriend
	}
	if blocked, err := s.BlockedEither(fromID, toID); err != nil {
		return "", false, err
	} else if blocked {
		return "", false, ErrFriendBlocked
	}
	state, err = s.FriendState(fromID, toID)
	if err != nil {
		return "", false, err
	}
	switch state {
	case "friends":
		return "", false, ErrAlreadyFriends
	case "outgoing":
		return "outgoing", false, nil
	case "incoming":
		if err := s.AcceptFriend(fromID, toID); err != nil {
			return "", false, err
		}
		return "friends", true, nil
	}
	res, err := s.DB.Exec(`INSERT INTO friend_requests (requester_id, addressee_id, status, created_at) VALUES (?,?, 'pending', ?) ON CONFLICT DO NOTHING`, fromID, toID, Now())
	if err != nil {
		return "", false, fmt.Errorf("friend request %d->%d: %w", fromID, toID, err)
	}
	n, _ := res.RowsAffected()
	return "outgoing", n > 0, nil
}

// PendingIncoming counts unanswered requests waiting for a person, optionally
// only those from one server.
func (s *Store) PendingIncoming(userID int64, host string) (int, error) {
	var n int
	q := `SELECT COUNT(*) FROM friend_requests f JOIN users u ON u.id = f.requester_id WHERE f.addressee_id = ? AND f.status = 'pending'`
	args := []any{userID}
	if host != "" {
		q += ` AND u.remote_host = ?`
		args = append(args, strings.ToLower(host))
	}
	if err := s.DB.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("pending incoming uid=%d: %w", userID, err)
	}
	return n, nil
}

func (s *Store) AcceptFriend(addresseeID, requesterID int64) error {
	res, err := s.DB.Exec(`UPDATE friend_requests SET status = 'accepted', responded_at = ? WHERE requester_id = ? AND addressee_id = ? AND status = 'pending'`, Now(), requesterID, addresseeID)
	if err != nil {
		return fmt.Errorf("accept friend %d<-%d: %w", addresseeID, requesterID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// EndFriendship removes a friendship, a request you received, or one you sent.
func (s *Store) EndFriendship(userID, otherID int64) error {
	if _, err := s.DB.Exec(`DELETE FROM friend_requests WHERE (requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`, userID, otherID, otherID, userID); err != nil {
		return fmt.Errorf("end friendship %d-%d: %w", userID, otherID, err)
	}
	return nil
}

func (s *Store) people(query string, args ...any) ([]Person, error) {
	rows, err := s.DB.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Username, &p.DisplayName, &p.AvatarPath); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const personSelect = `SELECT u.id, u.username, u.display_name, u.avatar_path FROM friend_requests f JOIN users u ON u.id = `

func (s *Store) Friends(userID int64) ([]Person, error) {
	return s.people(personSelect+`CASE WHEN f.requester_id = ? THEN f.addressee_id ELSE f.requester_id END
	  WHERE f.status = 'accepted' AND (f.requester_id = ? OR f.addressee_id = ?) AND u.is_banned = 0 ORDER BY u.username COLLATE NOCASE`, userID, userID, userID)
}

func (s *Store) IncomingFriendRequests(userID int64) ([]Person, error) {
	return s.people(personSelect+`f.requester_id WHERE f.addressee_id = ? AND f.status = 'pending' AND u.is_banned = 0 ORDER BY f.created_at DESC`, userID)
}

func (s *Store) OutgoingFriendRequests(userID int64) ([]Person, error) {
	return s.people(personSelect+`f.addressee_id WHERE f.requester_id = ? AND f.status = 'pending' AND u.is_banned = 0 ORDER BY f.created_at DESC`, userID)
}

func (s *Store) PendingFriendCount(userID int64) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM friend_requests WHERE addressee_id = ? AND status = 'pending'`, userID).Scan(&n); err != nil {
		return 0, fmt.Errorf("pending friend count uid=%d: %w", userID, err)
	}
	return n, nil
}

type FeedEvent struct {
	Kind          string
	UserID        int64
	Username      string
	DisplayName   string
	AvatarPath    string
	ShowTMDB      int64
	ShowName      string
	PosterPath    string
	EpisodeID     int64
	Season        int
	Episode       int
	Count         int
	Stars         int
	Excerpt       string
	At            string
	ViewerWatched bool
	ShowID        int64
}

// Remote says the person lives on another server.
func (e FeedEvent) Remote() bool { return strings.Contains(e.Username, "@") }

func (e FeedEvent) Name() string {
	if strings.TrimSpace(e.DisplayName) != "" {
		return e.DisplayName
	}
	return e.Username
}

const feedGroupGap = 30 * time.Minute

const spoilerFree = `c.deleted_at IS NULL AND c.author_spoiler = 0 AND NOT (c.spoiler_reports >= 1 AND c.not_spoiler_reports <= c.spoiler_reports)`

func (s *Store) notBlocked(col string) string {
	return `NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ? AND b.blocked_id = ` + col + `) OR (b.blocker_id = ` + col + ` AND b.blocked_id = ?))`
}

// FriendFeed is the timeline of what the viewer's friends did: what they
// watched, how they rated it, and their comments that are not spoilers.
// Consecutive watches of one show are folded into a single line. Which episode
// someone watched or rated is only named to a viewer who has seen it.
func (s *Store) FriendFeed(viewerID int64, before string, limit int) ([]FeedEvent, error) {
	friends, err := s.Friends(viewerID)
	if err != nil {
		return nil, err
	}
	if len(friends) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(friends))
	for _, f := range friends {
		ids = append(ids, f.ID)
	}
	in, inArgs := inClause(ids)
	if before == "" {
		before = "9999-12-31 00:00:00"
	}
	var all []FeedEvent

	wargs := append([]any{viewerID}, inArgs...)
	wargs = append(wargs, before, limit*6)
	rows, err := s.DB.Query(`SELECT w.user_id, u.username, u.display_name, u.avatar_path, s.id, s.tmdb_id, s.name, s.poster_path, e.id, e.season_number, e.episode_number, w.watched_at,
	    EXISTS (SELECT 1 FROM watched vw WHERE vw.user_id = ? AND vw.episode_id = e.id)
	  FROM watched w JOIN users u ON u.id = w.user_id JOIN episodes e ON e.id = w.episode_id JOIN shows s ON s.id = w.show_id
	  WHERE w.user_id IN (`+in+`) AND u.share_activity = 1 AND u.is_banned = 0 AND w.watched_at < ?
	  ORDER BY w.watched_at DESC LIMIT ?`, wargs...)
	if err != nil {
		return nil, fmt.Errorf("feed watches uid=%d: %w", viewerID, err)
	}
	for rows.Next() {
		ev := FeedEvent{Kind: "watch", Count: 1}
		var seen int
		if err := rows.Scan(&ev.UserID, &ev.Username, &ev.DisplayName, &ev.AvatarPath, &ev.ShowID, &ev.ShowTMDB, &ev.ShowName, &ev.PosterPath, &ev.EpisodeID, &ev.Season, &ev.Episode, &ev.At, &seen); err != nil {
			rows.Close()
			return nil, err
		}
		ev.ViewerWatched = seen == 1
		all = append(all, ev)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	all = groupWatches(all)

	rargs := append([]any{viewerID}, inArgs...)
	rargs = append(rargs, before, limit)
	rrows, err := s.DB.Query(`SELECT r.user_id, u.username, u.display_name, u.avatar_path, s.id, s.tmdb_id, s.name, s.poster_path, e.id, e.season_number, e.episode_number, r.rating, r.updated_at,
	    EXISTS (SELECT 1 FROM watched vw WHERE vw.user_id = ? AND vw.episode_id = e.id)
	  FROM episode_ratings r JOIN users u ON u.id = r.user_id JOIN episodes e ON e.id = r.episode_id JOIN shows s ON s.id = e.show_id
	  WHERE r.user_id IN (`+in+`) AND u.share_activity = 1 AND u.is_banned = 0 AND r.updated_at < ?
	  ORDER BY r.updated_at DESC LIMIT ?`, rargs...)
	if err != nil {
		return nil, fmt.Errorf("feed ratings uid=%d: %w", viewerID, err)
	}
	for rrows.Next() {
		ev := FeedEvent{Kind: "rating"}
		var seen, rating int
		if err := rrows.Scan(&ev.UserID, &ev.Username, &ev.DisplayName, &ev.AvatarPath, &ev.ShowID, &ev.ShowTMDB, &ev.ShowName, &ev.PosterPath, &ev.EpisodeID, &ev.Season, &ev.Episode, &rating, &ev.At, &seen); err != nil {
			rrows.Close()
			return nil, err
		}
		ev.Stars = (rating + 1) / 2
		ev.ViewerWatched = seen == 1
		all = append(all, ev)
	}
	if err := rrows.Err(); err != nil {
		rrows.Close()
		return nil, err
	}
	rrows.Close()

	xargs := append([]any{viewerID}, inArgs...)
	xargs = append(xargs, Now(), before, limit)
	xrows, err := s.DB.Query(`SELECT re.user_id, u.username, u.display_name, re.kind, re.show_tmdb, re.show_name, re.poster, re.season, re.episode, re.count, re.stars, re.excerpt, re.at,
	    EXISTS (SELECT 1 FROM watched vw JOIN episodes e ON e.id = vw.episode_id JOIN shows s ON s.id = e.show_id
	      WHERE vw.user_id = ? AND s.tmdb_id = re.show_tmdb AND e.season_number = re.season AND e.episode_number = re.episode)
	  FROM remote_events re JOIN users u ON u.id = re.user_id
	  WHERE re.user_id IN (`+in+`) AND re.expires_at > ? AND re.at < ? AND u.is_banned = 0
	  ORDER BY re.at DESC LIMIT ?`, xargs...)
	if err != nil {
		return nil, fmt.Errorf("feed remote uid=%d: %w", viewerID, err)
	}
	for xrows.Next() {
		var ev FeedEvent
		var seen int
		if err := xrows.Scan(&ev.UserID, &ev.Username, &ev.DisplayName, &ev.Kind, &ev.ShowTMDB, &ev.ShowName, &ev.PosterPath, &ev.Season, &ev.Episode, &ev.Count, &ev.Stars, &ev.Excerpt, &ev.At, &seen); err != nil {
			xrows.Close()
			return nil, err
		}
		ev.ViewerWatched = seen == 1
		all = append(all, ev)
	}
	if err := xrows.Err(); err != nil {
		xrows.Close()
		return nil, err
	}
	xrows.Close()

	cargs := append([]any{}, inArgs...)
	cargs = append(cargs, before, viewerID, viewerID, limit)
	crows, err := s.DB.Query(`SELECT c.user_id, u.username, u.display_name, u.avatar_path, s.id, s.tmdb_id, s.name, s.poster_path, e.id, e.season_number, e.episode_number, c.body, c.created_at
	  FROM comments c JOIN users u ON u.id = c.user_id JOIN episodes e ON e.id = c.episode_id JOIN shows s ON s.id = e.show_id
	  WHERE c.user_id IN (`+in+`) AND `+spoilerFree+` AND u.share_activity = 1 AND u.is_banned = 0 AND c.created_at < ?
	    AND `+s.notBlocked("c.user_id")+`
	  ORDER BY c.created_at DESC LIMIT ?`, cargs...)
	if err != nil {
		return nil, fmt.Errorf("feed comments uid=%d: %w", viewerID, err)
	}
	for crows.Next() {
		ev := FeedEvent{Kind: "comment"}
		if err := crows.Scan(&ev.UserID, &ev.Username, &ev.DisplayName, &ev.AvatarPath, &ev.ShowID, &ev.ShowTMDB, &ev.ShowName, &ev.PosterPath, &ev.EpisodeID, &ev.Season, &ev.Episode, &ev.Excerpt, &ev.At); err != nil {
			crows.Close()
			return nil, err
		}
		ev.Excerpt = excerpt(ev.Excerpt, 240)
		all = append(all, ev)
	}
	if err := crows.Err(); err != nil {
		crows.Close()
		return nil, err
	}
	crows.Close()

	pargs := append([]any{}, inArgs...)
	pargs = append(pargs, before, limit)
	prows, err := s.DB.Query(`SELECT c.user_id, u.username, u.display_name, u.avatar_path, c.body, c.created_at
	  FROM comments c JOIN users u ON u.id = c.user_id
	  WHERE c.profile_id = c.user_id AND c.parent_id IS NULL AND c.user_id IN (`+in+`) AND `+spoilerFree+` AND u.share_activity = 1 AND u.is_banned = 0 AND c.created_at < ?
	  ORDER BY c.created_at DESC LIMIT ?`, pargs...)
	if err != nil {
		return nil, fmt.Errorf("feed posts uid=%d: %w", viewerID, err)
	}
	for prows.Next() {
		ev := FeedEvent{Kind: "post"}
		if err := prows.Scan(&ev.UserID, &ev.Username, &ev.DisplayName, &ev.AvatarPath, &ev.Excerpt, &ev.At); err != nil {
			prows.Close()
			return nil, err
		}
		ev.Excerpt = excerpt(ev.Excerpt, 400)
		all = append(all, ev)
	}
	if err := prows.Err(); err != nil {
		prows.Close()
		return nil, err
	}
	prows.Close()

	sort.SliceStable(all, func(i, j int) bool { return all[i].At > all[j].At })
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// RecentOpenComments is what a person with no friends sees instead: a sample
// of recent comments from across the instance that are not spoilers.
func (s *Store) RecentOpenComments(viewerID int64, limit int) ([]FeedEvent, error) {
	since := Ts(time.Now().Add(-14 * 24 * time.Hour))
	rows, err := s.DB.Query(`SELECT c.user_id, u.username, u.display_name, u.avatar_path, s.id, s.tmdb_id, s.name, s.poster_path, e.id, e.season_number, e.episode_number, c.body, c.created_at
	  FROM comments c JOIN users u ON u.id = c.user_id JOIN episodes e ON e.id = c.episode_id JOIN shows s ON s.id = e.show_id
	  WHERE `+spoilerFree+` AND u.is_banned = 0 AND c.user_id <> ? AND c.created_at > ? AND `+s.notBlocked("c.user_id")+`
	  ORDER BY RANDOM() LIMIT ?`, viewerID, since, viewerID, viewerID, limit)
	if err != nil {
		return nil, fmt.Errorf("open comments uid=%d: %w", viewerID, err)
	}
	defer rows.Close()
	var out []FeedEvent
	for rows.Next() {
		ev := FeedEvent{Kind: "comment"}
		if err := rows.Scan(&ev.UserID, &ev.Username, &ev.DisplayName, &ev.AvatarPath, &ev.ShowID, &ev.ShowTMDB, &ev.ShowName, &ev.PosterPath, &ev.EpisodeID, &ev.Season, &ev.Episode, &ev.Excerpt, &ev.At); err != nil {
			return nil, err
		}
		ev.Excerpt = excerpt(ev.Excerpt, 240)
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out, rows.Err()
}

func excerpt(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "..."
}

// groupWatches folds runs of watches by one person of one show, close together
// in time, into a single event carrying the count and the furthest episode.
func groupWatches(in []FeedEvent) []FeedEvent {
	var out []FeedEvent
	for _, ev := range in {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Kind == "watch" && last.UserID == ev.UserID && last.ShowID == ev.ShowID {
				lt, e1 := time.Parse("2006-01-02 15:04:05", last.At)
				et, e2 := time.Parse("2006-01-02 15:04:05", ev.At)
				if e1 == nil && e2 == nil && lt.Sub(et) <= feedGroupGap {
					last.Count++
					continue
				}
			}
		}
		out = append(out, ev)
	}
	return out
}

type UserReport struct {
	ReportedID int64
	Username   string
	Reporters  int
	Reasons    string
	Last       string
}

// ReportUser files a report against a person's profile for the moderators.
func (s *Store) ReportUser(reporterID, reportedID int64, reason string) error {
	if reporterID == reportedID {
		return ErrSelfReport
	}
	if _, err := s.DB.Exec(`INSERT INTO user_reports (reporter_id, reported_id, reason, created_at) VALUES (?,?,?,?)`, reporterID, reportedID, reason, Now()); err != nil {
		if isUnique(err, "user_reports") {
			return ErrAlreadyReported
		}
		return fmt.Errorf("report user %d by %d: %w", reportedID, reporterID, err)
	}
	return nil
}

func (s *Store) OpenUserReports(limit int) ([]UserReport, error) {
	rows, err := s.DB.Query(`SELECT u.id, u.username, COUNT(*), COALESCE(GROUP_CONCAT(NULLIF(r.reason,''), ' | '),''), MAX(r.created_at)
	  FROM user_reports r JOIN users u ON u.id = r.reported_id
	  WHERE r.resolved_at IS NULL GROUP BY u.id ORDER BY COUNT(*) DESC, MAX(r.created_at) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("open user reports: %w", err)
	}
	defer rows.Close()
	var out []UserReport
	for rows.Next() {
		var r UserReport
		if err := rows.Scan(&r.ReportedID, &r.Username, &r.Reporters, &r.Reasons, &r.Last); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ResolveUserReports(reportedID int64) error {
	if _, err := s.DB.Exec(`UPDATE user_reports SET resolved_at = ? WHERE reported_id = ? AND resolved_at IS NULL`, Now(), reportedID); err != nil {
		return fmt.Errorf("resolve user reports %d: %w", reportedID, err)
	}
	return nil
}
