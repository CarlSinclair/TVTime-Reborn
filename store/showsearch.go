package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"tvt/logger"

	"github.com/CarlSinclair/csuit-libs/redis"
	"github.com/CarlSinclair/csuit-libs/searchkit"
	"github.com/CarlSinclair/csuit-libs/searchkit/redisindex"
)

const showSearchTimeout = 3 * time.Second

var showIndexWarnedAt atomic.Int64

func showIndex() *redisindex.Index {
	return &redisindex.Index{
		Name:   redis.Key("idx:shows"),
		Prefix: redis.Key("show:"),
		Fields: []redisindex.Field{
			{Name: "name", Kind: redisindex.Text, Weight: 3},
			{Name: "original", Kind: redisindex.Text, Weight: 2},
			{Name: "votes", Kind: redisindex.Numeric, Sortable: true},
		},
		Client: redis.Client,
	}
}

func showDoc(name, original string, votes int) redisindex.Doc {
	return redisindex.Doc{
		Text:    map[string]string{"name": name, "original": original},
		Nums:    map[string]float64{"votes": float64(votes)},
		Version: fmt.Sprintf("%s|%s|%d", name, original, votes),
	}
}

func warnShowIndexUnavailable(what string, err error) {
	now := time.Now().Unix()
	if last := showIndexWarnedAt.Load(); now-last < 600 || !showIndexWarnedAt.CompareAndSwap(last, now) {
		return
	}
	logger.Warn("show search index unavailable (%s), using the database scan: %v", what, err)
}

func (s *Store) reindexShow(id int64) {
	if redis.Client() == nil {
		return
	}
	var name, original string
	var votes int
	if err := s.DB.QueryRow(`SELECT name, original_name, vote_count FROM shows WHERE id = ?`, id).Scan(&name, &original, &votes); err != nil {
		logger.Error("show search index: load show=%d failed: %v", id, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), showSearchTimeout)
	defer cancel()
	ix := showIndex()
	if err := ix.Ensure(ctx); err != nil {
		warnShowIndexUnavailable("ensure", err)
		return
	}
	if err := ix.Upsert(ctx, strconv.FormatInt(id, 10), showDoc(name, original, votes)); err != nil {
		logger.Error("show search index: upsert show=%d failed, the daily reconcile will repair it: %v", id, err)
	}
}

// ReconcileShowIndex makes the search index match the shows table, touching
// only shows that are missing, changed or gone. It does nothing when no Redis
// with a query engine is available; searches then scan the table instead.
func (s *Store) ReconcileShowIndex() error {
	if redis.Client() == nil {
		return nil
	}
	rows, err := s.DB.Query(`SELECT id, name, original_name, vote_count FROM shows`)
	if err != nil {
		return fmt.Errorf("show search index reconcile: load shows: %w", err)
	}
	defer rows.Close()
	want := map[string]string{}
	docs := map[string]redisindex.Doc{}
	for rows.Next() {
		var id int64
		var name, original string
		var votes int
		if err := rows.Scan(&id, &name, &original, &votes); err != nil {
			return fmt.Errorf("show search index reconcile: scan show: %w", err)
		}
		key := strconv.FormatInt(id, 10)
		d := showDoc(name, original, votes)
		want[key] = d.Version
		docs[key] = d
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("show search index reconcile: read shows: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	rep, err := showIndex().Reconcile(ctx, want, func(ids []string) (map[string]redisindex.Doc, error) {
		out := make(map[string]redisindex.Doc, len(ids))
		for _, id := range ids {
			out[id] = docs[id]
		}
		return out, nil
	})
	if err != nil {
		warnShowIndexUnavailable("reconcile", err)
		return fmt.Errorf("show search index reconcile: added=%d updated=%d removed=%d failed=%d: %w", rep.Added, rep.Updated, rep.Removed, rep.Failed, err)
	}
	if rep.Added+rep.Updated+rep.Removed > 0 {
		logger.Warn("show search index reconcile repaired added=%d updated=%d removed=%d of %d shows", rep.Added, rep.Updated, rep.Removed, len(want))
	}
	return nil
}

func (s *Store) indexedShowIDs(q string, limit int) ([]int64, bool) {
	if redis.Client() == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), showSearchTimeout)
	defer cancel()
	res, err := showIndex().Search(ctx, redisindex.Query{Text: q, SortBy: "votes", Desc: true, Limit: limit})
	if err != nil {
		warnShowIndexUnavailable("search", err)
		return nil, false
	}
	ids := make([]int64, 0, len(res.Hits))
	for _, h := range res.Hits {
		id, convErr := strconv.ParseInt(h.ID, 10, 64)
		if convErr != nil {
			logger.Warn("show search index: non-numeric id %q: %v", h.ID, convErr)
			continue
		}
		ids = append(ids, id)
	}
	return ids, true
}

func (s *Store) scannedShowIDs(q string, limit int) ([]int64, error) {
	rows, err := s.DB.Query(`SELECT id, name, original_name FROM shows ORDER BY vote_count DESC`)
	if err != nil {
		return nil, fmt.Errorf("search local shows q=%q: %w", q, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var name, original string
		if err := rows.Scan(&id, &name, &original); err != nil {
			return nil, fmt.Errorf("scan local show for search: %w", err)
		}
		if searchkit.Match(q, name, original) {
			ids = append(ids, id)
			if len(ids) >= limit {
				break
			}
		}
	}
	return ids, rows.Err()
}

func (s *Store) showsByIDs(ids []int64) ([]Show, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.DB.Query(`SELECT `+showCols+` FROM shows WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load shows by id ids=%v: %w", ids, err)
	}
	defer rows.Close()
	byID := make(map[int64]Show, len(ids))
	for rows.Next() {
		sh, err := scanShow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan local show: %w", err)
		}
		byID[sh.ID] = *sh
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Show, 0, len(ids))
	for _, id := range ids {
		if sh, ok := byID[id]; ok {
			out = append(out, sh)
		}
	}
	return out, nil
}

// SearchLocal finds shows whose name or original name starts with every word
// typed, most voted first. It uses the Redis search index when one is
// available and otherwise scans the table with the same word matching.
func (s *Store) SearchLocal(q string, limit int) ([]Show, error) {
	if limit <= 0 {
		limit = 30
	}
	if len(searchkit.Words(q)) == 0 {
		return nil, nil
	}
	ids, ok := s.indexedShowIDs(q, limit)
	if !ok {
		var err error
		if ids, err = s.scannedShowIDs(q, limit); err != nil {
			return nil, err
		}
	}
	return s.showsByIDs(ids)
}
