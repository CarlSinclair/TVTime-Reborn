package store

import (
	"sync"
	"time"

	"tvt/logger"
)

const upNextTTL = 5 * time.Minute

type upNextEntry struct {
	at    time.Time
	day   string
	limit int
	items []UpNextItem
}

// upNextCache keeps each person's home-page list for a few minutes. Computing it
// reads every aired episode of every show they follow, so repeated visits
// should not pay for that again. Anything that changes what they watch or
// follow drops their entry; a new episode appearing is picked up within the
// time limit.
type upNextCache struct {
	mu      sync.Mutex
	m       map[int64]upNextEntry
	ver     map[int64]uint64
	warming map[int64]bool
}

func (c *upNextCache) get(userID int64, limit int) ([]UpNextItem, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[userID]
	if !ok || e.day != today() || time.Since(e.at) > upNextTTL || e.limit < limit {
		return nil, false
	}
	n := len(e.items)
	if n > limit {
		n = limit
	}
	return append([]UpNextItem(nil), e.items[:n]...), true
}

func (c *upNextCache) put(userID int64, limit int, items []UpNextItem) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 5000 {
		c.m = map[int64]upNextEntry{}
	}
	c.m[userID] = upNextEntry{at: time.Now(), day: today(), limit: limit, items: append([]UpNextItem(nil), items...)}
}

func (c *upNextCache) drop(userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if userID == 0 {
		c.m, c.ver = nil, nil
		return
	}
	delete(c.m, userID)
	if c.ver == nil {
		c.ver = map[int64]uint64{}
	}
	c.ver[userID]++
}

func (c *upNextCache) version(userID int64) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ver[userID]
}

func (c *upNextCache) putIfCurrent(userID int64, ver uint64, limit int, items []UpNextItem) {
	c.mu.Lock()
	current := c.ver[userID] == ver
	c.mu.Unlock()
	if current {
		c.put(userID, limit, items)
	}
}

func (c *upNextCache) startWarm(userID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.warming == nil {
		c.warming = map[int64]bool{}
	}
	if c.warming[userID] {
		return false
	}
	c.warming[userID] = true
	return true
}

func (c *upNextCache) endWarm(userID int64) {
	c.mu.Lock()
	delete(c.warming, userID)
	c.mu.Unlock()
}

// changed drops a person's list and rebuilds it in the background a moment
// later, so the next visit to the home page finds it ready. A burst of changes
// (marking a season) rebuilds it once.
func (s *Store) upNextChanged(userID int64) {
	s.up.drop(userID)
	if userID == 0 || !s.up.startWarm(userID) {
		return
	}
	go func() {
		defer s.up.endWarm(userID)
		time.Sleep(400 * time.Millisecond)
		ver := s.up.version(userID)
		items, err := s.upNext(userID, 0, 60)
		if err != nil {
			logger.Error("UPNEXT rebuild uid=%d: %v", userID, err)
			return
		}
		s.up.putIfCurrent(userID, ver, 60, items)
	}()
}
