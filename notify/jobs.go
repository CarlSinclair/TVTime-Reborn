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
	groupAlertsMin         = 2
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
// passed; when groupAlertsMin or more episodes of one season of a show are
// due together for a follower they get a single notification instead: "new
// season" when the whole season shares that air date, "new episodes" for a
// show released in parts. Each pair is recorded before sending so a crash can at worst skip an
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
	type due struct {
		alert store.EpisodeAlert
		user  *store.User
	}
	type seasonKey struct {
		user, show int64
		season     int
		date       string
	}
	users := map[int64]*store.User{}
	var ready []due
	size := map[seasonKey]int{}
	for _, a := range pending {
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
		ready = append(ready, due{a, u})
		size[seasonKey{a.UserID, a.ShowID, a.Season, a.AirDate}]++
	}
	sent := map[int64]int{}
	grouped := map[seasonKey]bool{}
	wholeSeason := map[seasonKey]int{}
	total := 0
	for _, d := range ready {
		a, u := d.alert, d.user
		key := seasonKey{a.UserID, a.ShowID, a.Season, a.AirDate}
		if size[key] >= groupAlertsMin {
			if grouped[key] {
				continue
			}
			grouped[key] = true
			if sent[a.UserID] >= maxAlertsPerUserPerRun {
				continue
			}
			marked := 0
			for _, other := range ready {
				o := other.alert
				if o.UserID != a.UserID || o.ShowID != a.ShowID || o.Season != a.Season || o.AirDate != a.AirDate {
					continue
				}
				if err := s.Store.MarkEpisodeNotified(o.UserID, o.EpisodeID); err != nil {
					logger.Error("NOTIFY episode alerts mark uid=%d ep=%d season digest show=%d season=%d: %v", o.UserID, o.EpisodeID, o.ShowID, o.Season, err)
					continue
				}
				marked++
			}
			if marked == 0 {
				continue
			}
			event, count := "episodes_aired", marked
			whole, ok := wholeSeason[key]
			if !ok {
				var err error
				whole, err = s.Store.SeasonDropSize(a.ShowID, a.Season, a.AirDate)
				if err != nil {
					logger.Error("NOTIFY episode alerts season check uid=%d show=%d season=%d, sending as new episodes: %v", a.UserID, a.ShowID, a.Season, err)
				}
				wholeSeason[key] = whole
			}
			if whole > 0 {
				event, count = "season_aired", whole
			}
			s.Send(a.UserID, event, fmt.Sprintf("/show/%d/season/%d", a.ShowTMDB, a.Season), map[string]any{
				"show": s.localShowName(u, a), "season": a.Season, "count": count,
			})
			sent[a.UserID]++
			total++
			continue
		}
		if sent[a.UserID] >= maxAlertsPerUserPerRun {
			continue
		}
		if err := s.Store.MarkEpisodeNotified(a.UserID, a.EpisodeID); err != nil {
			logger.Error("NOTIFY episode alerts mark uid=%d ep=%d: %v", a.UserID, a.EpisodeID, err)
			continue
		}
		epName := a.Name
		if u.Locale != "en" {
			if t, err := s.Store.EpisodeTexts(u.Locale, []int64{a.EpisodeID}); err == nil && t[a.EpisodeID].Name != "" {
				epName = t[a.EpisodeID].Name
			} else if err != nil {
				logger.Warn("NOTIFY episode alerts episode text uid=%d ep=%d lang=%s: %v", u.ID, a.EpisodeID, u.Locale, err)
			}
		}
		s.Send(a.UserID, "episode_aired", fmt.Sprintf("/episode/%d", a.EpisodeID), map[string]any{
			"show": s.localShowName(u, a), "code": fmt.Sprintf("S%02dE%02d", a.Season, a.Episode), "name": epName,
		})
		sent[a.UserID]++
		total++
	}
	if total > 0 {
		logger.Info("NOTIFY episode alerts sent=%d pending=%d", total, len(pending))
	}
}

func (s *Service) localShowName(u *store.User, a store.EpisodeAlert) string {
	if u.Locale == "en" {
		return a.ShowName
	}
	t, err := s.Store.ShowTexts(u.Locale, []int64{a.ShowID})
	if err != nil {
		logger.Warn("NOTIFY episode alerts show text uid=%d show=%d lang=%s: %v", u.ID, a.ShowID, u.Locale, err)
		return a.ShowName
	}
	if t[a.ShowID].Name != "" {
		return t[a.ShowID].Name
	}
	return a.ShowName
}
