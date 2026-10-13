package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrAlreadyReported = errors.New("already reported")
	ErrSelfReport      = errors.New("cannot report own comment")
	ErrForbidden       = errors.New("forbidden")
)

const MaxCommentLen = 2000

// Target identifies what a comment thread hangs off: an episode, or a person's
// profile, where the person's own top-level comments are their posts and
// everyone else replies.
type Target struct {
	Kind string
	ID   int64
}

const (
	KindEpisode = "episode"
	KindProfile = "profile"
)

func EpisodeTarget(id int64) Target { return Target{Kind: KindEpisode, ID: id} }
func ProfileTarget(id int64) Target { return Target{Kind: KindProfile, ID: id} }

func (t Target) col() string {
	if t.Kind == KindProfile {
		return "profile_id"
	}
	return "episode_id"
}

type Comment struct {
	ID                int64
	Target            Target
	UserID            int64
	Username          string
	DisplayName       string
	ParentID          int64
	Body              string
	AuthorSpoiler     bool
	SpoilerReports    int
	NotSpoilerReports int
	CreatedAt         string
	EditedAt          string
	Deleted           bool
	Hidden            bool
	// ParentHost and ParentRef name the other server's comment this one answers.
	ParentHost, ParentRef string
	MyReports             map[string]bool
}

// IsSpoiler is the display rule: the author's own tag always stands; otherwise
// a single spoiler report is enough, and only a larger number of "not a
// spoiler" reports than spoiler reports clears it.
func (c *Comment) IsSpoiler() bool {
	if c.AuthorSpoiler {
		return true
	}
	return c.SpoilerReports >= 1 && c.NotSpoilerReports <= c.SpoilerReports
}

func (s *Store) AddComment(userID int64, t Target, parentID int64, body string, spoiler bool) (int64, error) {
	var parent any
	if parentID != 0 {
		var pt int64
		var pp sql.NullInt64
		err := s.DB.QueryRow(`SELECT COALESCE(`+t.col()+`,0), parent_id FROM comments WHERE id = ? AND deleted_at IS NULL AND hidden_at IS NULL`, parentID).Scan(&pt, &pp)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, fmt.Errorf("lookup parent comment=%d: %w", parentID, err)
		}
		if pt != t.ID {
			return 0, ErrNotFound
		}
		if pp.Valid {
			parentID = pp.Int64
		}
		parent = parentID
	}
	sp := 0
	if spoiler {
		sp = 1
	}
	src := "episodes"
	if t.Kind == KindProfile {
		src = "users"
	}
	res, err := s.DB.Exec(`INSERT INTO comments (`+t.col()+`, user_id, parent_id, body, author_spoiler, created_at)
	  SELECT id, ?, ?, ?, ?, ? FROM `+src+` WHERE id = ?`, userID, parent, body, sp, Now(), t.ID)
	if err != nil {
		return 0, fmt.Errorf("add comment uid=%d %s=%d: %w", userID, t.Kind, t.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return res.LastInsertId()
}

const commentSelect = `SELECT c.id, COALESCE(c.episode_id,0), COALESCE(c.profile_id,0), c.user_id, u.username, u.display_name, COALESCE(c.parent_id,0), c.body,
	    c.author_spoiler, c.spoiler_reports, c.not_spoiler_reports, c.created_at, COALESCE(c.edited_at,''), c.deleted_at IS NOT NULL, c.hidden_at IS NOT NULL, c.parent_host, c.parent_ref
	  FROM comments c JOIN users u ON u.id = c.user_id `

func scanComment(row interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var ep, prof int64
	var as, del, hid int
	if err := row.Scan(&c.ID, &ep, &prof, &c.UserID, &c.Username, &c.DisplayName, &c.ParentID, &c.Body, &as, &c.SpoilerReports, &c.NotSpoilerReports,
		&c.CreatedAt, &c.EditedAt, &del, &hid, &c.ParentHost, &c.ParentRef); err != nil {
		return c, err
	}
	if prof != 0 {
		c.Target = ProfileTarget(prof)
	} else {
		c.Target = EpisodeTarget(ep)
	}
	c.AuthorSpoiler = as == 1
	c.Deleted = del == 1
	c.Hidden = hid == 1 && !c.Deleted
	if c.Deleted || c.Hidden {
		c.Body = ""
	}
	return c, nil
}

const notBlocked = `AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ? AND b.blocked_id = c.user_id) OR (b.blocker_id = c.user_id AND b.blocked_id = ?))`

// Comments returns one page of top-level threads (newest first) followed by
// each thread's replies. More reports whether another page exists.
func (s *Store) Comments(t Target, viewerID int64, limit, offset int) (list []Comment, more bool, err error) {
	rows, err := s.DB.Query(commentSelect+`WHERE c.`+t.col()+` = ? AND c.parent_id IS NULL AND c.parent_ref = '' `+notBlocked+`
	    AND ((c.deleted_at IS NULL AND c.hidden_at IS NULL) OR EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = c.id AND r.deleted_at IS NULL AND r.hidden_at IS NULL))
	  ORDER BY c.created_at DESC, c.id DESC LIMIT ? OFFSET ?`, t.ID, viewerID, viewerID, limit+1, offset)
	if err != nil {
		return nil, false, fmt.Errorf("list comments %s=%d: %w", t.Kind, t.ID, err)
	}
	var tops []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		tops = append(tops, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(tops) > limit {
		more = true
		tops = tops[:limit]
	}
	if len(tops) == 0 {
		return nil, false, nil
	}
	ids := make([]any, 0, len(tops)+2)
	marks := make([]string, 0, len(tops))
	for _, c := range tops {
		ids = append(ids, c.ID)
		marks = append(marks, "?")
	}
	args := append(ids, viewerID, viewerID)
	rrows, err := s.DB.Query(commentSelect+`WHERE c.parent_id IN (`+strings.Join(marks, ",")+`) AND c.deleted_at IS NULL AND c.hidden_at IS NULL `+notBlocked+` ORDER BY c.created_at ASC, c.id ASC`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list replies %s=%d: %w", t.Kind, t.ID, err)
	}
	replies := map[int64][]Comment{}
	for rrows.Next() {
		c, err := scanComment(rrows)
		if err != nil {
			rrows.Close()
			return nil, false, err
		}
		replies[c.ParentID] = append(replies[c.ParentID], c)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, false, err
	}
	for _, c := range tops {
		list = append(list, c)
		list = append(list, replies[c.ID]...)
	}
	if viewerID != 0 {
		mine := map[int64]map[string]bool{}
		rr, err := s.DB.Query(`SELECT r.comment_id, r.kind FROM comment_reports r JOIN comments c ON c.id = r.comment_id WHERE r.user_id = ? AND c.`+t.col()+` = ?`, viewerID, t.ID)
		if err != nil {
			return nil, false, fmt.Errorf("list own reports %s=%d uid=%d: %w", t.Kind, t.ID, viewerID, err)
		}
		for rr.Next() {
			var id int64
			var kind string
			if err := rr.Scan(&id, &kind); err != nil {
				rr.Close()
				return nil, false, err
			}
			if mine[id] == nil {
				mine[id] = map[string]bool{}
			}
			mine[id][kind] = true
		}
		rr.Close()
		if err := rr.Err(); err != nil {
			return nil, false, fmt.Errorf("list own reports %s=%d uid=%d: %w", t.Kind, t.ID, viewerID, err)
		}
		for i := range list {
			list[i].MyReports = mine[list[i].ID]
		}
	}
	return list, more, nil
}

func (s *Store) CountComments(t Target) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE `+t.col()+` = ? AND deleted_at IS NULL AND hidden_at IS NULL`, t.ID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count comments %s=%d: %w", t.Kind, t.ID, err)
	}
	return n, nil
}

func (s *Store) CommentByID(id int64) (*Comment, error) {
	c, err := scanComment(s.DB.QueryRow(commentSelect+`WHERE c.id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("comment by id=%d: %w", id, err)
	}
	return &c, nil
}

func (s *Store) EditComment(userID, id int64, body string, spoiler bool) error {
	sp := 0
	if spoiler {
		sp = 1
	}
	res, err := s.DB.Exec(`UPDATE comments SET body = ?, author_spoiler = ?, edited_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL AND hidden_at IS NULL`, body, sp, Now(), id, userID)
	if err != nil {
		return fmt.Errorf("edit comment id=%d uid=%d: %w", id, userID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrForbidden
	}
	return nil
}

func (s *Store) DeleteComment(actorID int64, admin bool, id int64) error {
	q := `UPDATE comments SET deleted_at = ?, body = '' WHERE id = ? AND deleted_at IS NULL`
	args := []any{Now(), id}
	if !admin {
		q += ` AND user_id = ?`
		args = append(args, actorID)
	}
	res, err := s.DB.Exec(q, args...)
	if err != nil {
		return fmt.Errorf("delete comment id=%d actor=%d: %w", id, actorID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrForbidden
	}
	return nil
}

func (s *Store) ReportComment(userID, commentID int64, kind, reason string) error {
	if kind != "spoiler" && kind != "not_spoiler" && kind != "abuse" {
		return fmt.Errorf("invalid report kind %q", kind)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin report: %w", err)
	}
	defer tx.Rollback()
	var owner int64
	if err := tx.QueryRow(`SELECT user_id FROM comments WHERE id = ? AND deleted_at IS NULL`, commentID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("report lookup comment=%d: %w", commentID, err)
	}
	if owner == userID {
		return ErrSelfReport
	}
	res, err := tx.Exec(`INSERT INTO comment_reports (comment_id, user_id, kind, reason, created_at) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`,
		commentID, userID, kind, reason, Now())
	if err != nil {
		return fmt.Errorf("insert report comment=%d uid=%d kind=%s: %w", commentID, userID, kind, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAlreadyReported
	}
	switch kind {
	case "spoiler":
		_, err = tx.Exec(`UPDATE comments SET spoiler_reports = spoiler_reports + 1 WHERE id = ?`, commentID)
	case "not_spoiler":
		_, err = tx.Exec(`UPDATE comments SET not_spoiler_reports = not_spoiler_reports + 1 WHERE id = ?`, commentID)
	}
	if err != nil {
		return fmt.Errorf("update report counts comment=%d kind=%s: %w", commentID, kind, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit report comment=%d: %w", commentID, err)
	}
	return nil
}

func (s *Store) Block(blockerID int64, username string) error {
	u, err := s.UserByUsername(username)
	if err != nil {
		return err
	}
	if u.ID == blockerID {
		return ErrSelfReport
	}
	return s.blockUser(blockerID, u.ID)
}

// blockUser records the block and ends any friendship or pending request
// between the two, so a blocked person has no way to reach the blocker.
func (s *Store) blockUser(blockerID, targetID int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin block uid=%d target=%d: %w", blockerID, targetID, err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO blocks (blocker_id, blocked_id, created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`, blockerID, targetID, Now()); err != nil {
		return fmt.Errorf("block uid=%d target=%d: %w", blockerID, targetID, err)
	}
	if _, err := tx.Exec(`DELETE FROM friend_requests WHERE (requester_id = ? AND addressee_id = ?) OR (requester_id = ? AND addressee_id = ?)`, blockerID, targetID, targetID, blockerID); err != nil {
		return fmt.Errorf("end friendship on block uid=%d target=%d: %w", blockerID, targetID, err)
	}
	return tx.Commit()
}

func (s *Store) BlockByComment(blockerID, commentID int64) error {
	var owner int64
	if err := s.DB.QueryRow(`SELECT user_id FROM comments WHERE id = ?`, commentID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("block lookup comment=%d: %w", commentID, err)
	}
	if owner == blockerID {
		return ErrSelfReport
	}
	return s.blockUser(blockerID, owner)
}

func (s *Store) Unblock(blockerID int64, username string) error {
	u, err := s.UserByUsername(username)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`DELETE FROM blocks WHERE blocker_id = ? AND blocked_id = ?`, blockerID, u.ID); err != nil {
		return fmt.Errorf("unblock uid=%d target=%d: %w", blockerID, u.ID, err)
	}
	return nil
}

func (s *Store) Blocked(blockerID int64) ([]string, error) {
	rows, err := s.DB.Query(`SELECT u.username FROM blocks b JOIN users u ON u.id = b.blocked_id WHERE b.blocker_id = ? ORDER BY u.username`, blockerID)
	if err != nil {
		return nil, fmt.Errorf("list blocks uid=%d: %w", blockerID, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type AbuseReport struct {
	CommentID   int64
	Target      Target
	ProfileName string
	Body        string
	Author      string
	AuthorID    int64
	Reporters   int
	Reasons     string
	Last        string
}

func (s *Store) OpenAbuseReports(limit int) ([]AbuseReport, error) {
	rows, err := s.DB.Query(`SELECT c.id, COALESCE(c.episode_id,0), COALESCE(c.profile_id,0), COALESCE((SELECT username FROM users WHERE id = c.profile_id),''), c.body, u.username, u.id, COUNT(*), COALESCE(GROUP_CONCAT(NULLIF(r.reason,''), ' | '),''), MAX(r.created_at)
	  FROM comment_reports r JOIN comments c ON c.id = r.comment_id JOIN users u ON u.id = c.user_id
	  WHERE r.kind = 'abuse' AND r.resolved_at IS NULL AND c.deleted_at IS NULL AND c.hidden_at IS NULL
	  GROUP BY c.id ORDER BY COUNT(*) DESC, MAX(r.created_at) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("open abuse reports: %w", err)
	}
	defer rows.Close()
	var out []AbuseReport
	for rows.Next() {
		var a AbuseReport
		var ep, prof int64
		if err := rows.Scan(&a.CommentID, &ep, &prof, &a.ProfileName, &a.Body, &a.Author, &a.AuthorID, &a.Reporters, &a.Reasons, &a.Last); err != nil {
			return nil, err
		}
		if prof != 0 {
			a.Target = ProfileTarget(prof)
		} else {
			a.Target = EpisodeTarget(ep)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) ResolveReports(commentID int64) error {
	if _, err := s.DB.Exec(`UPDATE comment_reports SET resolved_at = ? WHERE comment_id = ? AND kind = 'abuse' AND resolved_at IS NULL`, Now(), commentID); err != nil {
		return fmt.Errorf("resolve reports comment=%d: %w", commentID, err)
	}
	return nil
}

// BlockedEither reports whether either user has blocked the other.
func (s *Store) BlockedEither(a, b int64) (bool, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM blocks WHERE (blocker_id = ? AND blocked_id = ?) OR (blocker_id = ? AND blocked_id = ?)`, a, b, b, a).Scan(&n); err != nil {
		return false, fmt.Errorf("blocked-either check a=%d b=%d: %w", a, b, err)
	}
	return n > 0, nil
}

// HideUserComments hides everything a person has written without losing it:
// the comments show as removed until RestoreComments brings them back.
func (s *Store) HideUserComments(userID int64) error {
	if _, err := s.DB.Exec(`UPDATE comments SET hidden_at = ? WHERE user_id = ? AND deleted_at IS NULL AND hidden_at IS NULL`, Now(), userID); err != nil {
		return fmt.Errorf("hide comments uid=%d: %w", userID, err)
	}
	return nil
}

// RestoreComments undoes HideUserComments once the person is neither banned nor
// deactivated any more.
func (s *Store) RestoreComments(userID int64) error {
	if _, err := s.DB.Exec(`UPDATE comments SET hidden_at = NULL WHERE user_id = ? AND hidden_at IS NOT NULL
	  AND EXISTS (SELECT 1 FROM users WHERE id = ? AND is_banned = 0 AND is_deactivated = 0)`, userID, userID); err != nil {
		return fmt.Errorf("restore comments uid=%d: %w", userID, err)
	}
	return nil
}

// EraseUserComments deletes the text of everything a person has written. The
// comments stay as "Deleted comment" so replies under them keep their place.
func (s *Store) EraseUserComments(userID int64) error {
	if _, err := s.DB.Exec(`UPDATE comments SET deleted_at = ?, body = '' WHERE user_id = ? AND deleted_at IS NULL`, Now(), userID); err != nil {
		return fmt.Errorf("erase comments uid=%d: %w", userID, err)
	}
	return nil
}

// tombstoneUser is the account that deleted people's erased comments are moved
// to, so they can outlive the account without naming anyone.
func (s *Store) tombstoneUser() (int64, error) {
	var id int64
	err := s.DB.QueryRow(`SELECT id FROM users WHERE username = 'deleted' AND remote_host = ''`).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("find tombstone user: %w", err)
	}
	res, err := s.DB.Exec(`INSERT INTO users (username, display_name, is_deactivated, created_at) VALUES ('deleted', 'Anonymous', 1, ?)`, Now())
	if err != nil {
		return 0, fmt.Errorf("create tombstone user: %w", err)
	}
	return res.LastInsertId()
}

// TransferUserComments moves everything a person has written to the tombstone
// account, ready for their own account to be deleted without taking anyone's
// replies with it. mode says what happens to the comments themselves first:
// "delete" erases the text, "hide" hides them, anything else keeps them.
func (s *Store) TransferUserComments(userID int64, mode string) error {
	switch mode {
	case "delete":
		if err := s.EraseUserComments(userID); err != nil {
			return err
		}
	case "hide":
		if err := s.HideUserComments(userID); err != nil {
			return err
		}
	}
	tomb, err := s.tombstoneUser()
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(`UPDATE comments SET user_id = ? WHERE user_id = ? AND profile_id IS NOT ?`, tomb, userID, userID); err != nil {
		return fmt.Errorf("move comments of uid=%d to the deleted account: %w", userID, err)
	}
	return nil
}

// OpenComment is a local episode comment that is visible to everyone, with what
// another server needs to show it next to the right episode.
type OpenComment struct {
	ID              int64
	UserID          int64
	Username        string
	DisplayName     string
	ParentID        int64
	Body            string
	Spoiler         bool
	CreatedAt       string
	EditedAt        string
	ShowTMDB        int64
	Season, Episode int
	ParentHost      string
	ParentRef       string
}

// OpenEpisodeComment returns a comment only while it is shown to everyone: not
// deleted, not hidden and written by someone who is not suspended.
func (s *Store) OpenEpisodeComment(id int64) (*OpenComment, error) {
	var c OpenComment
	var sp int
	err := s.DB.QueryRow(`SELECT c.id, c.user_id, u.username, u.display_name, COALESCE(c.parent_id,0), c.body, c.author_spoiler, c.created_at, COALESCE(c.edited_at,''), sh.tmdb_id, e.season_number, e.episode_number, c.parent_host, c.parent_ref
	  FROM comments c JOIN users u ON u.id = c.user_id JOIN episodes e ON e.id = c.episode_id JOIN shows sh ON sh.id = e.show_id
	  WHERE c.id = ? AND c.deleted_at IS NULL AND c.hidden_at IS NULL AND u.is_deactivated = 0 AND u.remote_host = ''`, id).Scan(
		&c.ID, &c.UserID, &c.Username, &c.DisplayName, &c.ParentID, &c.Body, &sp, &c.CreatedAt, &c.EditedAt, &c.ShowTMDB, &c.Season, &c.Episode, &c.ParentHost, &c.ParentRef)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("open episode comment %d: %w", id, err)
	}
	c.Spoiler = sp == 1
	return &c, nil
}

// UserEpisodeCommentIDs lists the episode comments someone wrote that are still
// shown, so they can be withdrawn from other servers.
func (s *Store) UserEpisodeCommentIDs(userID int64) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT id FROM comments WHERE user_id = ? AND episode_id IS NOT NULL AND deleted_at IS NULL AND hidden_at IS NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("list episode comments uid=%d: %w", userID, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AddRemoteReply stores a comment that answers a comment on another server.
func (s *Store) AddRemoteReply(userID, episodeID int64, parentHost, parentRef, body string, spoiler bool) (int64, error) {
	sp := 0
	if spoiler {
		sp = 1
	}
	res, err := s.DB.Exec(`INSERT INTO comments (episode_id, user_id, parent_host, parent_ref, body, author_spoiler, created_at)
	  SELECT id, ?, ?, ?, ?, ?, ? FROM episodes WHERE id = ?`, userID, strings.ToLower(parentHost), parentRef, body, sp, Now(), episodeID)
	if err != nil {
		return 0, fmt.Errorf("add remote reply uid=%d episode=%d: %w", userID, episodeID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return res.LastInsertId()
}

// RemoteParentReplies lists the comments on an episode that answer comments on
// other servers, so they can be shown under their parents.
func (s *Store) RemoteParentReplies(episodeID, viewerID int64) ([]Comment, error) {
	rows, err := s.DB.Query(commentSelect+`WHERE c.episode_id = ? AND c.parent_ref <> '' `+notBlocked+` ORDER BY c.created_at ASC, c.id ASC`, episodeID, viewerID, viewerID)
	if err != nil {
		return nil, fmt.Errorf("remote parent replies episode=%d: %w", episodeID, err)
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
