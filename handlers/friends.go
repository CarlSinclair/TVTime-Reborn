package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"tvt/auth"
	"tvt/logger"
	"tvt/store"
)

type personView struct {
	Username string `json:"username"`
	Name     string `json:"name"`
	Avatar   string `json:"avatar"`
	State    string `json:"state,omitempty"`
	Self     bool   `json:"self,omitempty"`
}

func (a *App) personJSON(p store.Person) personView {
	v := personView{Username: p.Username, Name: p.Name()}
	if p.AvatarPath != "" {
		v.Avatar = a.mediaURL("avatars", p.AvatarPath)
	}
	return v
}

// localHandle reads what someone typed to find a person. A bare name, "@name"
// and "name@here" all mean a person on this server; the host of the base URL
// works too. Anything else names another server, which is not supported yet.
func (a *App) localHandle(q string) (name string, local bool) {
	q = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	if q == "" {
		return "", true
	}
	name, host, hasHost := strings.Cut(q, "@")
	if !hasHost {
		return strings.TrimSpace(name), true
	}
	host = strings.ToLower(strings.TrimSpace(host))
	own := ""
	if u, err := url.Parse(a.Cfg.BaseURL); err == nil {
		own = strings.ToLower(u.Hostname())
	}
	if host == "here" || host == "" || (own != "" && host == own) {
		return strings.TrimSpace(name), true
	}
	return strings.TrimSpace(name), false
}

// findPerson is an exact username lookup that hides banned people and anyone
// in a block with the viewer, so a blocked person sees an account that does not
// exist.
func (a *App) findPerson(viewer *store.User, q string) (*store.User, bool, error) {
	name, local := a.localHandle(q)
	if !local {
		return nil, true, nil
	}
	if !store.ValidUsername(name) {
		return nil, false, nil
	}
	u, err := a.Store.UserByUsername(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if u.IsBanned {
		return nil, false, nil
	}
	if viewer != nil && viewer.ID != u.ID {
		blocked, err := a.Store.BlockedEither(viewer.ID, u.ID)
		if err != nil {
			return nil, false, err
		}
		if blocked {
			return nil, false, nil
		}
	}
	return u, false, nil
}

func (a *App) apiPeople(w http.ResponseWriter, r *http.Request) {
	viewer, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.searchLimit, "p:"+itoa(viewer.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	u, remote, err := a.findPerson(viewer, r.URL.Query().Get("q"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]any{"remote": remote, "results": []personView{}}
	if u != nil {
		pv := a.personJSON(store.Person{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AvatarPath: u.AvatarPath})
		pv.Self = u.ID == viewer.ID
		if !pv.Self {
			if pv.State, err = a.Store.FriendState(viewer.ID, u.ID); err != nil {
				a.fail(w, r, err)
				return
			}
		}
		out["results"] = []personView{pv}
	}
	a.okJSON(w, out)
}

func (a *App) apiFriendsList(w http.ResponseWriter, r *http.Request) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	friends, err := a.Store.Friends(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	incoming, err := a.Store.IncomingFriendRequests(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	outgoing, err := a.Store.OutgoingFriendRequests(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	conv := func(list []store.Person) []personView {
		out := make([]personView, 0, len(list))
		for _, p := range list {
			out = append(out, a.personJSON(p))
		}
		return out
	}
	a.okJSON(w, map[string]any{"friends": conv(friends), "incoming": conv(incoming), "outgoing": conv(outgoing)})
}

func (a *App) friendTarget(w http.ResponseWriter, r *http.Request) (*store.User, *store.User, bool) {
	viewer, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return nil, nil, false
	}
	var in struct{ Username string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return nil, nil, false
	}
	target, remote, err := a.findPerson(viewer, in.Username)
	switch {
	case err != nil:
		a.fail(w, r, err)
		return nil, nil, false
	case remote:
		a.fail(w, r, apiErr{http.StatusNotImplemented, "err.friend_remote"})
		return nil, nil, false
	case target == nil:
		a.fail(w, r, errNotFound)
		return nil, nil, false
	}
	return viewer, target, true
}

func (a *App) apiFriendRequest(w http.ResponseWriter, r *http.Request) {
	viewer, target, ok := a.friendTarget(w, r)
	if !ok {
		return
	}
	if err := a.allow(a.writeLimit, "fr:"+itoa(viewer.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	state, err := a.Store.RequestFriend(viewer.ID, target.ID)
	switch {
	case errors.Is(err, store.ErrSelfFriend):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.friend_self"})
		return
	case errors.Is(err, store.ErrFriendBlocked):
		a.fail(w, r, errNotFound)
		return
	case errors.Is(err, store.ErrAlreadyFriends):
		a.okJSON(w, map[string]any{"state": "friends"})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	switch state {
	case "outgoing":
		a.Notif.Send(target.ID, "friend_request", "/?friends=1", map[string]any{"user": viewer.Name(), "username": viewer.Username})
	case "friends":
		a.Notif.Send(target.ID, "friend_accepted", "/u/"+viewer.Username, map[string]any{"user": viewer.Name()})
	}
	logger.Info("FRIENDS request uid=%d target=%d state=%s", viewer.ID, target.ID, state)
	a.okJSON(w, map[string]any{"state": state})
}

func (a *App) apiFriendRespond(w http.ResponseWriter, r *http.Request) {
	viewer, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct {
		Username string
		Accept   bool
	}
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	target, remote, err := a.findPerson(viewer, in.Username)
	if err != nil || remote || target == nil {
		a.fail(w, r, errNotFound)
		return
	}
	if !in.Accept {
		state, err := a.Store.FriendState(viewer.ID, target.ID)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if state == "incoming" {
			if err := a.Store.EndFriendship(viewer.ID, target.ID); err != nil {
				a.fail(w, r, err)
				return
			}
		}
		a.okJSON(w, nil)
		return
	}
	if err := a.Store.AcceptFriend(viewer.ID, target.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.fail(w, r, errNotFound)
		} else {
			a.fail(w, r, err)
		}
		return
	}
	a.Notif.Send(target.ID, "friend_accepted", "/u/"+viewer.Username, map[string]any{"user": viewer.Name()})
	a.okJSON(w, map[string]any{"state": "friends"})
}

func (a *App) apiFriendRemove(w http.ResponseWriter, r *http.Request) {
	viewer, target, ok := a.friendTarget(w, r)
	if !ok {
		return
	}
	if err := a.Store.EndFriendship(viewer.ID, target.ID); err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, nil)
}

func (a *App) apiBlockUser(w http.ResponseWriter, r *http.Request) {
	viewer, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Username string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	switch err := a.Store.Block(viewer.ID, in.Username); {
	case err == nil:
		logger.Info("BLOCK uid=%d blocked %q", viewer.ID, in.Username)
		a.okJSON(w, nil)
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, r, errNotFound)
	case errors.Is(err, store.ErrSelfReport):
		a.fail(w, r, errBadRequest)
	default:
		a.fail(w, r, err)
	}
}

func (a *App) apiReportUser(w http.ResponseWriter, r *http.Request) {
	viewer, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var in struct{ Username, Reason string }
	if err := decode(r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.allow(a.reportLimit, "r:"+itoa(viewer.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	target, remote, err := a.findPerson(viewer, in.Username)
	if err != nil || remote || target == nil {
		a.fail(w, r, errNotFound)
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if r := []rune(reason); len(r) > 300 {
		reason = string(r[:300])
	}
	switch err := a.Store.ReportUser(viewer.ID, target.ID, reason); {
	case err == nil:
		logger.Info("REPORT user=%d by uid=%d", target.ID, viewer.ID)
		a.Notif.SendToAdmins("user_report", "/admin/reports", map[string]any{"user": viewer.Name()})
		a.okJSON(w, nil)
	case errors.Is(err, store.ErrAlreadyReported):
		a.fail(w, r, apiErr{http.StatusConflict, "err.already_reported"})
	case errors.Is(err, store.ErrSelfReport):
		a.fail(w, r, apiErr{http.StatusBadRequest, "err.own_comment"})
	default:
		a.fail(w, r, err)
	}
}

func (a *App) apiFeed(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		a.fail(w, r, errUnauth)
		return
	}
	html, next, mode, err := a.feedHTML(w, r, u, r.URL.Query().Get("before"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"html": html, "next": next, "mode": mode})
}
