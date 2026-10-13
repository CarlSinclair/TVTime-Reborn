package notify

import (
	"context"
	"fmt"
	"time"

	"tvt/logger"
	"tvt/store"
)

// RunJobs runs the periodic background work: new-episode alerts every few
// minutes and a daily prune of old notifications.
func (s *Service) RunJobs(ctx context.Context) {
	logger.Info("NOTIFY jobs started")
	alerts := time.NewTicker(10 * time.Minute)
	prune := time.NewTicker(24 * time.Hour)
	defer alerts.Stop()
	defer prune.Stop()
	s.EpisodeAlerts(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-alerts.C:
			s.EpisodeAlerts(now)
		case <-prune.C:
			n, err := s.Store.PruneNotifications(time.Now().AddDate(0, 0, -90))
			if err != nil {
				logger.Error("NOTIFY prune: %v", err)
			} else if n > 0 {
				logger.Info("NOTIFY pruned %d old notifications", n)
			}
		}
	}
}

const (
	maxAlertsPerUserPerRun = 10
	alertStaleAfter        = 48 * time.Hour
)

// alertFireTime is when a follower should be told about an episode. When a
// real broadcast time is known (TVmaze, else Trakt) that exact instant is used.
// When only the air date is known the anchor is midday local time on that date,
// which is late enough not to alert hours before a typical release and early
// enough to still be useful the same day. The user's offset (negative = before)
// is applied to either.
func alertFireTime(airDate, airAt string, u *store.User) (time.Time, bool) {
	offset := time.Duration(u.NotifyOffsetMin) * time.Minute
	if airAt != "" {
		if t := store.ParseTs(airAt); !t.IsZero() {
			return t.Add(offset), true
		}
	}
	loc := u.Location()
	d, err := time.ParseInLocation("2006-01-02", airDate, loc)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc).Add(offset), true
}

// EpisodeAlerts notifies each follower once per episode when its fire time has
// passed. Each pair is recorded before sending so a crash can at worst skip an
// alert, never repeat it. Anything more than 48 hours late is dropped so
// following an old show, or changing the offset, never floods the inbox.
func (s *Service) EpisodeAlerts(now time.Time) {
	window := store.MaxNotifyOffsetMin/(24*60) + 3
	from := now.UTC().AddDate(0, 0, -window).Format("2006-01-02")
	to := now.UTC().AddDate(0, 0, window).Format("2006-01-02")
	pending, err := s.Store.PendingEpisodeAlerts(from, to)
	if err != nil {
		logger.Error("NOTIFY episode alerts query: %v", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	sent := map[int64]int{}
	users := map[int64]*store.User{}
	total := 0
	for _, a := range pending {
		if sent[a.UserID] >= maxAlertsPerUserPerRun {
			continue
		}
		u, ok := users[a.UserID]
		if !ok {
			loaded, err := s.Store.UserByID(a.UserID)
			if err != nil {
				logger.Error("NOTIFY episode alerts load user uid=%d: %v", a.UserID, err)
				continue
			}
			u = loaded
			users[a.UserID] = u
		}
		fire, ok := alertFireTime(a.AirDate, a.AirAt, u)
		if !ok {
			logger.Error("NOTIFY episode alerts bad air date uid=%d air_date=%q", u.ID, a.AirDate)
			continue
		}
		if fire.After(now) || now.Sub(fire) > alertStaleAfter {
			continue
		}
		if err := s.Store.MarkEpisodeNotified(a.UserID, a.EpisodeID); err != nil {
			logger.Error("NOTIFY episode alerts mark: %v", err)
			continue
		}
		showName, epName := a.ShowName, a.Name
		if u.Locale != "en" {
			if t, err := s.Store.ShowTexts(u.Locale, []int64{a.ShowID}); err == nil && t[a.ShowID].Name != "" {
				showName = t[a.ShowID].Name
			}
			if t, err := s.Store.EpisodeTexts(u.Locale, []int64{a.EpisodeID}); err == nil && t[a.EpisodeID].Name != "" {
				epName = t[a.EpisodeID].Name
			}
		}
		s.Send(a.UserID, "episode_aired", fmt.Sprintf("/episode/%d", a.EpisodeID), map[string]any{
			"show": showName, "code": fmt.Sprintf("S%02dE%02d", a.Season, a.Episode), "name": epName,
		})
		sent[a.UserID]++
		total++
	}
	if total > 0 {
		logger.Info("NOTIFY episode alerts sent=%d pending=%d", total, len(pending))
	}
}
