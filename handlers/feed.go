package handlers

import (
	"strings"

	"net/http"

	"tvt/logger"
	"tvt/store"
)

const feedPage = 25

// feedHTML renders one page of the home feed. mode says what it is: "friends"
// for a friend timeline, "suggested" for the sample of open comments shown to
// someone with no friends, "none" when there is nothing at all.
func (a *App) feedHTML(w http.ResponseWriter, r *http.Request, u *store.User, before string) (html, next, mode string, err error) {
	friends, err := a.Store.Friends(u.ID)
	if err != nil {
		return "", "", "", err
	}
	var events []store.FeedEvent
	mode = "friends"
	if len(friends) == 0 {
		mode = "suggested"
		if before == "" {
			events, err = a.Store.RecentOpenComments(u.ID, 20)
		}
	} else {
		events, err = a.Store.FriendFeed(u.ID, before, feedPage)
	}
	if err != nil {
		return "", "", "", err
	}
	if len(events) == 0 && before == "" && mode == "friends" {
		mode = "none"
	}
	lang := a.lang(w, r, u)
	if lang != "en" && len(events) > 0 {
		shows := make([]*store.Show, len(events))
		for i, ev := range events {
			shows[i] = &store.Show{ID: ev.ShowID, Name: ev.ShowName}
		}
		if err := a.Store.LocalizeShows(lang, shows...); err != nil {
			logger.Error("LOCALIZE feed shows lang=%s: %v", lang, err)
		} else {
			for i := range events {
				events[i].ShowName = shows[i].Name
			}
		}
	}
	v := &View{App: a, Lang: lang, User: u}
	var buf strings.Builder
	for _, ev := range events {
		if err := a.tmpl["home"].ExecuteTemplate(&buf, "feed_item", map[string]any{"E": ev, "V": v}); err != nil {
			logger.Error("API feed render: %v", err)
			return "", "", "", err
		}
	}
	if mode == "friends" && len(events) == feedPage {
		next = events[len(events)-1].At
	}
	return buf.String(), next, mode, nil
}
