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

// Target identifies what a comment thread hangs off; only episodes have one.
type Target struct {
	ID int64
}

func EpisodeTarget(id int64) Target { return Target{ID: id} }

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
	MyReports         map[string]bool
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
		err := s.DB.QueryRow(`SELECT episode_id, parent_id FROM comments WHERE id = ? AND deleted_at IS NULL`, parentID).Scan(&pt, &pp)
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
	res, err := s.DB.Exec(`INSERT INTO comments (episode_id, user_id, parent_id, body, author_spoiler, created_at)
	  SELECT id, ?, ?, ?, ?, ? FROM episodes WHERE id = ?`, userID, parent, body, sp, Now(), t.ID)
	if err != nil {
		return 0, fmt.Errorf("add comment uid=%d episode=%d: %w", userID, t.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return res.LastInsertId()
}

const commentSelect = `SELECT c.id, c.episode_id, c.user_id, u.username, u.display_name, COALESCE(c.parent_id,0), c.body,
	    c.author_spoiler, c.spoiler_reports, c.not_spoiler_reports, c.created_at, COALESCE(c.edited_at,''), c.deleted_at IS NOT NULL
	  FROM comments c JOIN users u ON u.id = c.user_id `

func scanComment(row interface{ Scan(...any) error }) (Comment, error) {
	var c Comment
	var ep int64
	var as, del int
	if err := row.Scan(&c.ID, &ep, &c.UserID, &c.Username, &c.DisplayName, &c.ParentID, &c.Body, &as, &c.SpoilerReports, &c.NotSpoilerReports,
		&c.CreatedAt, &c.EditedAt, &del); err != nil {
		return c, err
	}
	c.Target = EpisodeTarget(ep)
	c.AuthorSpoiler = as == 1
	c.Deleted = del == 1
	if c.Deleted {
		c.Body = ""
	}
	return c, nil
}

const notBlocked = `AND NOT EXISTS (SELECT 1 FROM blocks b WHERE (b.blocker_id = ? AND b.blocked_id = c.user_id) OR (b.blocker_id = c.user_id AND b.blocked_id = ?))`

// Comments returns one page of top-level threads (newest first) followed by
// each thread's replies. More reports whether another page exists.
func (s *Store) Comments(t Target, viewerID int64, limit, offset int) (list []Comment, more bool, err error) {
	rows, err := s.DB.Query(commentSelect+`WHERE c.episode_id = ? AND c.parent_id IS NULL `+notBlocked+`
	    AND (c.deleted_at IS NULL OR EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = c.id AND r.deleted_at IS NULL))
	  ORDER BY c.created_at DESC, c.id DESC LIMIT ? OFFSET ?`, t.ID, viewerID, viewerID, limit+1, offset)
	if err != nil {
		return nil, false, fmt.Errorf("list comments episode=%d: %w", t.ID, err)
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
	rrows, err := s.DB.Query(commentSelect+`WHERE c.parent_id IN (`+strings.Join(marks, ",")+`) AND c.deleted_at IS NULL `+notBlocked+` ORDER BY c.created_at ASC, c.id ASC`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list replies episode=%d: %w", t.ID, err)
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
		rr, err := s.DB.Query(`SELECT r.comment_id, r.kind FROM comment_reports r JOIN comments c ON c.id = r.comment_id WHERE r.user_id = ? AND c.episode_id = ?`, viewerID, t.ID)
		if err != nil {
			return nil, false, fmt.Errorf("list own reports episode=%d uid=%d: %w", t.ID, viewerID, err)
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
			return nil, false, fmt.Errorf("list own reports episode=%d uid=%d: %w", t.ID, viewerID, err)
		}
		for i := range list {
			list[i].MyReports = mine[list[i].ID]
		}
	}
	return list, more, nil
}

func (s *Store) CountComments(t Target) (int, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE episode_id = ? AND deleted_at IS NULL`, t.ID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count comments episode=%d: %w", t.ID, err)
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
	res, err := s.DB.Exec(`UPDATE comments SET body = ?, author_spoiler = ?, edited_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, body, sp, Now(), id, userID)
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
	if _, err := s.DB.Exec(`INSERT INTO blocks (blocker_id, blocked_id, created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`, blockerID, u.ID, Now()); err != nil {
		return fmt.Errorf("block uid=%d target=%d: %w", blockerID, u.ID, err)
	}
	return nil
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
	if _, err := s.DB.Exec(`INSERT INTO blocks (blocker_id, blocked_id, created_at) VALUES (?,?,?) ON CONFLICT DO NOTHING`, blockerID, owner, Now()); err != nil {
		return fmt.Errorf("block uid=%d target=%d: %w", blockerID, owner, err)
	}
	return nil
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
	CommentID int64
	Target    Target
	Body      string
	Author    string
	AuthorID  int64
	Reporters int
	Reasons   string
	Last      string
}

func (s *Store) OpenAbuseReports(limit int) ([]AbuseReport, error) {
	rows, err := s.DB.Query(`SELECT c.id, c.episode_id, c.body, u.username, u.id, COUNT(*), COALESCE(GROUP_CONCAT(NULLIF(r.reason,''), ' | '),''), MAX(r.created_at)
	  FROM comment_reports r JOIN comments c ON c.id = r.comment_id JOIN users u ON u.id = c.user_id
	  WHERE r.kind = 'abuse' AND r.resolved_at IS NULL AND c.deleted_at IS NULL
	  GROUP BY c.id ORDER BY COUNT(*) DESC, MAX(r.created_at) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("open abuse reports: %w", err)
	}
	defer rows.Close()
	var out []AbuseReport
	for rows.Next() {
		var a AbuseReport
		var ep int64
		if err := rows.Scan(&a.CommentID, &ep, &a.Body, &a.Author, &a.AuthorID, &a.Reporters, &a.Reasons, &a.Last); err != nil {
			return nil, err
		}
		a.Target = EpisodeTarget(ep)
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
