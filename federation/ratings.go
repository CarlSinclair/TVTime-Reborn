package federation

import (
	"time"

	"tvt/logger"
	"tvt/store"
)

const (
	ratingBatch          = 100
	maxRatingsPerMessage = 200
)

// RatingMsg is one rating as it travels between servers. Rating 0 takes the
// person's earlier rating of that thing back.
type RatingMsg struct {
	Kind     string `json:"kind"`
	ShowTMDB int64  `json:"show_tmdb"`
	Season   int    `json:"season,omitempty"`
	Episode  int    `json:"episode,omitempty"`
	Rating   int    `json:"rating"`
	At       string `json:"at,omitempty"`
}

func ratingMsgs(rs []store.Rating) []RatingMsg {
	at := store.Now()
	out := make([]RatingMsg, 0, len(rs))
	for _, r := range rs {
		m := RatingMsg{Kind: r.Kind, ShowTMDB: r.ShowTMDB, Season: r.Season, Episode: r.Episode, Rating: r.Rating, At: at}
		if validRating(m) {
			out = append(out, m)
		}
	}
	return out
}

func (s *Service) sendRatings(host string, u *store.User, rs []store.Rating) {
	msgs := ratingMsgs(rs)
	for len(msgs) > 0 {
		n := min(len(msgs), ratingBatch)
		s.enqueue(host, message{Type: "ratings", From: person(u), Ratings: msgs[:n]})
		msgs = msgs[n:]
	}
}

// ShareRatings sends a person's new, changed or withdrawn ratings to every
// allow-listed server.
func (s *Service) ShareRatings(u *store.User, rs []store.Rating) {
	if u == nil || u.IsRemote() || len(rs) == 0 {
		return
	}
	hosts, err := s.audience(u, false)
	if err != nil {
		return
	}
	for _, h := range hosts {
		s.sendRatings(h, u, rs)
	}
}

// WithdrawRatings takes every rating a person gave back from the other servers.
func (s *Service) WithdrawRatings(u *store.User) {
	if u == nil || u.IsRemote() {
		return
	}
	all, err := s.Store.LocalRatings(u.ID)
	if err != nil {
		logger.Error("FEDERATION withdraw ratings uid=%d: %v", u.ID, err)
		return
	}
	for i := range all {
		all[i].Rating = 0
	}
	hosts, err := s.audience(u, false)
	if err != nil {
		return
	}
	for _, h := range hosts {
		s.sendRatings(h, u, all)
	}
}

// BackfillRatings sends everything every local person has rated to one server, for when it is newly allow-listed.
func (s *Service) BackfillRatings(host string) {
	ids, err := s.Store.LocalUserIDs()
	if err != nil {
		logger.Error("FEDERATION backfill ratings for %s: %v", host, err)
		return
	}
	sent := 0
	for _, id := range ids {
		u, err := s.Store.UserByID(id)
		if err != nil {
			logger.Error("FEDERATION backfill ratings for %s: load uid=%d: %v", host, id, err)
			continue
		}
		rs, err := s.Store.LocalRatings(id)
		if err != nil {
			logger.Error("FEDERATION backfill ratings for %s: %v", host, err)
			continue
		}
		s.sendRatings(host, u, rs)
		sent += len(rs)
	}
	logger.Info("FEDERATION queued %d ratings for %s", sent, host)
}

func validRating(r RatingMsg) bool {
	if r.ShowTMDB < 1 || r.ShowTMDB > 1<<40 || r.Rating < 0 || r.Rating > 10 || r.Season < 0 || r.Season > 5000 || r.Episode < 0 || r.Episode > 100000 {
		return false
	}
	switch r.Kind {
	case "episode":
		return r.Episode >= 1
	case "season":
		return r.Episode == 0
	case "show":
		return r.Season == 0 && r.Episode == 0
	}
	return false
}

func (s *Service) onRatings(host string, m message) error {
	if len(m.Ratings) == 0 || len(m.Ratings) > maxRatingsPerMessage {
		return errBadMessage
	}
	for _, r := range m.Ratings {
		if !validRating(r) {
			return errBadMessage
		}
	}
	shared, err := s.Store.PeerShares(host)
	if err != nil || !shared {
		return err
	}
	shadow, err := s.Store.EnsureRemoteUser(host, m.From.Username, cleanName(m.From.Name))
	if err != nil {
		return err
	}
	if !shadow.IsRemote() || shadow.IsDeactivated {
		return nil
	}
	for _, r := range m.Ratings {
		at := r.At
		if _, err := time.Parse("2006-01-02 15:04:05", at); err != nil {
			at = store.Now()
		}
		rating := store.Rating{Kind: r.Kind, ShowTMDB: r.ShowTMDB, Season: r.Season, Episode: r.Episode, Rating: r.Rating}
		if err := s.Store.SaveRemoteRating(shadow.ID, rating, at); err != nil {
			return err
		}
	}
	return nil
}
