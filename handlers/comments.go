package handlers

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

func cleanBody(s string) (string, bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n")
	if s == "" || utf8.RuneCountInString(s) > store.MaxCommentLen || !utf8.ValidString(s) {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", false
		}
	}
	return s, true
}

type commentJSON struct {
	ID        int64           `json:"id"`
	User      string          `json:"user"`
	Name      string          `json:"name"`
	Body      string          `json:"body"`
	Spoiler   bool            `json:"spoiler"`
	ParentID  int64           `json:"parent_id"`
	CreatedAt string          `json:"created_at"`
	Edited    bool            `json:"edited"`
	Deleted   bool            `json:"deleted"`
	Hidden    bool            `json:"hidden"`
	Mine      bool            `json:"mine"`
	Reported  map[string]bool `json:"reported"`
	Host      string          `json:"host,omitempty"`
}

// remoteComments is what the other servers contribute to one episode's
// discussion: whole threads of their own, and replies to comments written here.
type remoteComments struct {
	threads [][]commentJSON
	// replies answer local comments; ParentID is that local comment's id.
	replies []commentJSON
	// byRef finds a remote comment's (negative) id from its host and ref, so
	// local replies to it can be attached.
	byRef map[string]int64
}

// remoteThreads gathers the other servers' comments on an episode. Their ids are
// negative so they never collide with a local comment's. A reply to a reply is
// grouped under the top of its thread, as local replies are.
func (a *App) remoteThreads(t store.Target, uid int64) *remoteComments {
	if t.Kind != store.KindEpisode {
		return nil
	}
	ep, err := a.Store.Episode(t.ID, 0)
	if err != nil {
		return nil
	}
	sh := a.showOfEpisode(ep)
	if sh == nil {
		return nil
	}
	list, err := a.Store.RemoteCommentsFor(sh.TMDBID, ep.SeasonNumber, ep.EpisodeNumber, uid)
	if err != nil {
		logger.Error("COMMENTS remote episode=%d: %v", t.ID, err)
		return nil
	}
	localHost := ""
	if a.Fed != nil {
		localHost = a.Fed.Host
	}
	out := &remoteComments{byRef: map[string]int64{}}
	for _, c := range list {
		out.byRef[c.Host+"|"+c.Ref] = -c.ID
	}
	root := map[int64]int64{}
	replies := map[int64][]commentJSON{}
	var tops []commentJSON
	for _, c := range list {
		name := c.Name
		if name == "" {
			name = c.Handle
			if i := strings.LastIndex(name, "@"); i > 0 {
				name = name[:i]
			}
		}
		j := commentJSON{ID: -c.ID, User: c.Handle, Name: name, Host: c.Host, Body: c.Body, Spoiler: c.Spoiler, CreatedAt: c.CreatedAt, Edited: c.Edited != ""}
		parentHost := c.ParentHost
		if parentHost == "" {
			parentHost = c.Host
		}
		switch {
		case c.ParentRef == "":
			root[j.ID] = j.ID
			tops = append(tops, j)
		case parentHost == localHost && localHost != "":
			if id, err := strconv.ParseInt(strings.TrimPrefix(c.ParentRef, "c:"), 10, 64); err == nil {
				j.ParentID = id
				out.replies = append(out.replies, j)
			}
		default:
			pid, ok := out.byRef[parentHost+"|"+c.ParentRef]
			if !ok {
				continue
			}
			top := root[pid]
			if top == 0 {
				continue
			}
			root[j.ID] = top
			j.ParentID = top
			replies[top] = append(replies[top], j)
		}
	}
	for i := len(tops) - 1; i >= 0; i-- {
		out.threads = append(out.threads, append([]commentJSON{tops[i]}, replies[tops[i].ID]...))
	}
	return out
}

// weaveRemote merges the other servers' comments into a page of local comments
// by time, and puts replies under the comments they answer. A page of local
// threads covers a stretch of time; remote threads are placed within that
// stretch, so paging through shows each comment once.
func weaveRemote(local []commentJSON, rc *remoteComments, localReplies []store.Comment, uid int64, more bool, before string) []commentJSON {
	var threads [][]commentJSON
	for _, c := range local {
		if c.ParentID == 0 || len(threads) == 0 {
			threads = append(threads, []commentJSON{c})
		} else {
			threads[len(threads)-1] = append(threads[len(threads)-1], c)
		}
	}
	lower := ""
	if more && len(threads) > 0 {
		lower = threads[len(threads)-1][0].CreatedAt
	}
	for _, th := range rc.threads {
		at := th[0].CreatedAt
		if (before != "" && at >= before) || (lower != "" && at < lower) {
			continue
		}
		threads = append(threads, th)
	}
	// replies from other servers to local comments, and local replies to remote ones
	attach := func(parentID int64, c commentJSON) {
		for i, th := range threads {
			for _, m := range th {
				if m.ID == parentID {
					threads[i] = append(threads[i], c)
					return
				}
			}
		}
	}
	for _, c := range rc.replies {
		attach(c.ParentID, c)
	}
	for _, c := range localReplies {
		pid, ok := rc.byRef[c.ParentHost+"|"+c.ParentRef]
		if !ok {
			continue
		}
		name := c.DisplayName
		if name == "" {
			name = c.Username
		}
		attach(pid, commentJSON{ID: c.ID, User: c.Username, Name: name, Body: c.Body, Spoiler: c.IsSpoiler(), ParentID: pid, CreatedAt: c.CreatedAt,
			Edited: c.EditedAt != "", Deleted: c.Deleted, Hidden: c.Hidden, Mine: uid != 0 && c.UserID == uid})
	}
	for i := range threads {
		if len(threads[i]) > 2 {
			rest := threads[i][1:]
			sort.SliceStable(rest, func(a, b int) bool { return rest[a].CreatedAt < rest[b].CreatedAt })
		}
	}
	sort.SliceStable(threads, func(i, j int) bool { return threads[i][0].CreatedAt > threads[j][0].CreatedAt })
	var out []commentJSON
	for _, th := range threads {
		out = append(out, th...)
	}
	return out
}

const commentPage = 20

func (a *App) targetOf(r *http.Request) (store.Target, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return store.Target{}, errNotFound
	}
	if strings.HasPrefix(r.URL.Path, "/api/profile/") {
		return store.ProfileTarget(id), nil
	}
	return store.EpisodeTarget(id), nil
}

func (a *App) targetLink(t store.Target) string {
	if t.Kind == store.KindProfile {
		if u, err := a.Store.UserByID(t.ID); err == nil {
			return "/u/" + u.Username + "#comments"
		}
		return "/"
	}
	return "/episode/" + itoa(t.ID)
}

// canSeeProfileThread is who may read and reply to a person's posts: anyone
// when the profile is public, otherwise the owner, friends and moderators.
// Banned owners and blocks make the thread not exist.
func (a *App) canSeeProfileThread(viewer *store.User, ownerID int64) (*store.User, error) {
	owner, err := a.Store.UserByID(ownerID)
	if err != nil {
		return nil, errNotFound
	}
	if owner.IsDeactivated && (viewer == nil || !viewer.IsAdmin) {
		return nil, errNotFound
	}
	if viewer != nil && viewer.ID == owner.ID {
		return owner, nil
	}
	if viewer != nil {
		blocked, err := a.Store.BlockedEither(viewer.ID, owner.ID)
		if err != nil {
			return nil, err
		}
		if blocked {
			return nil, errNotFound
		}
		if viewer.IsAdmin {
			return owner, nil
		}
		if !owner.ProfilePublic {
			state, err := a.Store.FriendState(viewer.ID, owner.ID)
			if err != nil {
				return nil, err
			}
			if state != "friends" {
				return nil, errNotFound
			}
		}
		return owner, nil
	}
	if !owner.ProfilePublic {
		return nil, errNotFound
	}
	return owner, nil
}

func (a *App) viewerWatched(t store.Target, uid int64) (bool, error) {
	if t.Kind == store.KindProfile {
		return false, nil
	}
	if uid == 0 {
		return false, nil
	}
	ep, err := a.Store.Episode(t.ID, uid)
	if err != nil {
		return false, err
	}
	return ep.Watched, nil
}

func (a *App) apiListComments() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, err := a.targetOf(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		var uid int64
		viewer := auth.UserFrom(r.Context())
		if viewer != nil {
			uid = viewer.ID
		}
		if t.Kind == store.KindProfile {
			if _, err := a.canSeeProfileThread(viewer, t.ID); err != nil {
				a.fail(w, r, err)
				return
			}
		}
		watched, err := a.viewerWatched(t, uid)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				a.fail(w, r, errNotFound)
			} else {
				a.fail(w, r, err)
			}
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		list, more, err := a.Store.Comments(t, uid, commentPage, offset)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		total, err := a.Store.CountComments(t)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		out := make([]commentJSON, 0, len(list))
		for _, c := range list {
			name := c.DisplayName
			if name == "" {
				name = c.Username
			}
			out = append(out, commentJSON{
				ID: c.ID, User: c.Username, Name: name, Body: c.Body, Spoiler: c.IsSpoiler(), ParentID: c.ParentID,
				CreatedAt: c.CreatedAt, Edited: c.EditedAt != "", Deleted: c.Deleted, Hidden: c.Hidden, Mine: uid != 0 && c.UserID == uid, Reported: c.MyReports,
			})
		}
		if remote := a.remoteThreads(t, uid); remote != nil {
			var localReplies []store.Comment
			if t.Kind == store.KindEpisode {
				if localReplies, err = a.Store.RemoteParentReplies(t.ID, uid); err != nil {
					logger.Error("COMMENTS replies to remote comments episode=%d: %v", t.ID, err)
				}
			}
			out = weaveRemote(out, remote, localReplies, uid, more, r.URL.Query().Get("before"))
		}
		a.okJSON(w, map[string]any{"comments": out, "total": total, "viewer_watched": watched, "more": more, "next_offset": offset + commentPage})
	}
}

func (a *App) apiAddComment() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.needVerified(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		t, err := a.targetOf(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		var in struct {
			Body     string
			Spoiler  bool
			ParentID int64 `json:"parent_id"`
		}
		if err := decode(r, &in); err != nil {
			a.fail(w, r, err)
			return
		}
		if t.Kind == store.KindProfile {
			if _, err := a.canSeeProfileThread(u, t.ID); err != nil {
				a.fail(w, r, err)
				return
			}
			if in.ParentID == 0 && u.ID != t.ID {
				a.fail(w, r, errForbidden)
				return
			}
		}
		body, ok := cleanBody(in.Body)
		if !ok {
			a.fail(w, r, apiErr{http.StatusBadRequest, "err.comment_invalid"})
			return
		}
		if err := a.allow(a.commentLimit, "c:"+strconv.FormatInt(u.ID, 10)); err != nil {
			a.fail(w, r, err)
			return
		}
		if in.ParentID < 0 {
			a.replyToRemote(w, r, u, t, in.ParentID, body, in.Spoiler)
			return
		}
		cid, err := a.Store.AddComment(u.ID, t, in.ParentID, body, in.Spoiler)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				a.fail(w, r, errNotFound)
			} else {
				a.fail(w, r, err)
			}
			return
		}
		if in.ParentID != 0 {
			a.notifyReply(u, t, in.ParentID, body)
		}
		a.emitComment(u, t, in.ParentID, cid, body, in.Spoiler)
		a.okJSON(w, map[string]any{"id": cid})
	}
}

// replyToRemote answers a comment that lives on another server. The reply is
// stored here, shown under its parent, and shared with the other servers like any
// comment; the parent's home server tells its author.
func (a *App) replyToRemote(w http.ResponseWriter, r *http.Request, u *store.User, t store.Target, parentID int64, body string, spoiler bool) {
	if t.Kind != store.KindEpisode {
		a.fail(w, r, errBadRequest)
		return
	}
	rc, err := a.Store.RemoteCommentByID(-parentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	ep, err := a.Store.Episode(t.ID, 0)
	if err != nil {
		a.fail(w, r, errNotFound)
		return
	}
	sh := a.showOfEpisode(ep)
	if sh == nil || sh.TMDBID != rc.ShowTMDB || ep.SeasonNumber != rc.Season || ep.EpisodeNumber != rc.Episode {
		a.fail(w, r, errNotFound)
		return
	}
	if blocked, err := a.Store.BlockedEither(u.ID, rc.UserID); err != nil || blocked {
		if err != nil {
			a.fail(w, r, err)
		} else {
			a.fail(w, r, errNotFound)
		}
		return
	}
	cid, err := a.Store.AddRemoteReply(u.ID, t.ID, rc.Host, rc.Ref, body, spoiler)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.emitComment(u, t, 0, cid, body, spoiler)
	a.okJSON(w, map[string]any{"id": cid})
}

func (a *App) notifyReply(author *store.User, t store.Target, parentID int64, body string) {
	parent, err := a.Store.CommentByID(parentID)
	if err != nil {
		logger.Error("NOTIFY reply parent comment=%d: %v", parentID, err)
		return
	}
	if parent.UserID == author.ID {
		return
	}
	blocked, err := a.Store.BlockedEither(author.ID, parent.UserID)
	if err != nil {
		logger.Error("NOTIFY reply block check: %v", err)
		return
	}
	if blocked {
		return
	}
	a.Notif.Send(parent.UserID, "comment_reply", a.targetLink(t), map[string]any{"user": author.Name(), "excerpt": truncate(body, 90)})
}

func (a *App) commentID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, r, errNotFound)
		return 0, false
	}
	return id, true
}

func (a *App) apiEditComment(w http.ResponseWriter, r *http.Request) {
	u, err := a.needVerified(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, ok := a.commentID(w, r)
	if !ok {
		return
	}
	var in struct {
		Body    string
		Spoiler bool
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	body, ok := cleanBody(in.Body)
	if !ok {
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.comment_invalid"})
		return
	}
	if err := a.Store.EditComment(u.ID, id, body, in.Spoiler); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			a.fail(w, r, errForbidden)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	if c, err := a.Store.CommentByID(id); err == nil {
		a.emitComment(u, c.Target, c.ParentID, id, body, in.Spoiler)
	}
	a.okJSON(w, nil)
}

func (a *App) apiDeleteComment(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, ok := a.commentID(w, r)
	if !ok {
		return
	}
	if err := a.Store.DeleteComment(u.ID, false, id); err != nil {
		if errors.Is(err, store.ErrForbidden) {
			a.fail(w, r, errForbidden)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	if a.Fed != nil {
		a.Fed.Retract(u, "c:"+itoa(id))
		a.Fed.WithdrawComment(u, "c:"+itoa(id))
	}
	a.okJSON(w, nil)
}

func (a *App) apiReportComment(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, ok := a.commentID(w, r)
	if !ok {
		return
	}
	var in struct{ Kind, Reason string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Kind != "spoiler" && in.Kind != "not_spoiler" && in.Kind != "abuse" {
		a.fail(w, r, errBadRequest)
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if utf8.RuneCountInString(in.Reason) > 300 {
		in.Reason = string([]rune(in.Reason)[:300])
	}
	if err := a.allow(a.reportLimit, "r:"+strconv.FormatInt(u.ID, 10)); err != nil {
		a.fail(w, r, err)
		return
	}
	err = a.Store.ReportComment(u.ID, id, in.Kind, in.Reason)
	switch {
	case err == nil:
		logger.Info("REPORT comment=%d kind=%s by uid=%d", id, in.Kind, u.ID)
		if in.Kind == "abuse" {
			a.Notif.SendToAdmins("abuse_report", "/admin/reports", map[string]any{"user": u.Name()})
		}
		a.okJSON(w, nil)
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, r, errNotFound)
	case errors.Is(err, store.ErrAlreadyReported):
		a.fail(w, r, apiErr{http.StatusConflict, "err.already_reported"})
	case errors.Is(err, store.ErrSelfReport):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.own_comment"})
	default:
		a.fail(w, r, err)
	}
}

func (a *App) apiBlockAuthor(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	id, ok := a.commentID(w, r)
	if !ok {
		return
	}
	switch err := a.Store.BlockByComment(u.ID, id); {
	case err == nil:
		a.okJSON(w, nil)
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, r, errNotFound)
	case errors.Is(err, store.ErrSelfReport):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.own_comment"})
	default:
		a.fail(w, r, err)
	}
}

func (a *App) apiUnblock(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Username string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.Store.Unblock(u.ID, in.Username); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	a.okJSON(w, nil)
}
