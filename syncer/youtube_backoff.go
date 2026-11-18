package syncer

import (
	"errors"
	"time"
)

const (
	youtubeBackoffMin  = 10 * time.Minute
	youtubeBackoffMax  = 24 * time.Hour
	youtubeThrottleMin = 30 * time.Minute
	youtubeThrottleMax = 6 * time.Hour
	youtubeDateGap     = 500 * time.Millisecond
)

var errYouTubeThrottled = errors.New("youtube is throttling requests")

type ytBackoff struct {
	until time.Time
	delay time.Duration
}

func (b *ytBackoff) fail(now time.Time, min, max time.Duration) {
	switch {
	case b.delay == 0:
		b.delay = min
	case b.delay < max:
		b.delay *= 2
		if b.delay > max {
			b.delay = max
		}
	}
	b.until = now.Add(b.delay)
}

func (s *Syncer) ytBlockedUntil(showID int64) (time.Time, bool) {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	now := s.now()
	until := s.ytThrottle.until
	if r, ok := s.ytRetry[showID]; ok && r.until.After(until) {
		until = r.until
	}
	return until, now.Before(until)
}

func (s *Syncer) ytThrottled() bool {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	return s.now().Before(s.ytThrottle.until)
}

func (s *Syncer) ytTripThrottle() (tripped bool, until time.Time) {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	now := s.now()
	if now.Before(s.ytThrottle.until) {
		return false, s.ytThrottle.until
	}
	s.ytThrottle.fail(now, youtubeThrottleMin, youtubeThrottleMax)
	return true, s.ytThrottle.until
}

func (s *Syncer) ytShowFailed(showID int64) time.Time {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	if s.ytRetry == nil {
		s.ytRetry = map[int64]*ytBackoff{}
	}
	b := s.ytRetry[showID]
	if b == nil {
		b = &ytBackoff{}
		s.ytRetry[showID] = b
	}
	b.fail(s.now(), youtubeBackoffMin, youtubeBackoffMax)
	return b.until
}

func (s *Syncer) ytShowSucceeded(showID int64) {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	delete(s.ytRetry, showID)
	s.ytThrottle = ytBackoff{}
}

func (s *Syncer) ytCachedDate(id string) (time.Time, bool) {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	t, ok := s.ytDates[id]
	return t, ok
}

func (s *Syncer) ytStoreDate(id string, t time.Time) {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	if s.ytDates == nil {
		s.ytDates = map[string]time.Time{}
	}
	s.ytDates[id] = t
}

func (s *Syncer) ytForgetDates(ids []string) {
	s.ytMu.Lock()
	defer s.ytMu.Unlock()
	for _, id := range ids {
		delete(s.ytDates, id)
	}
}
