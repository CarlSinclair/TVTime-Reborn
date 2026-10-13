package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"tvt/logger"
	"tvt/notify"
	"tvt/store"
	"tvt/syncer"
	"tvt/tmdb"
	"tvt/tvdb"
)

const (
	SourceTVTime = "tvtime"
	jobTimeout   = 3 * time.Hour
)

type Runner struct {
	Store  *store.Store
	Sync   *syncer.Syncer
	TMDB   *tmdb.Client
	Notify *notify.Service

	slot sync.Mutex
}

// Start queues a job. Imports run one at a time on the whole server so a big
// import cannot starve TMDB's rate budget for everyone else.
func (r *Runner) Start(jobID int64) {
	go func() {
		r.slot.Lock()
		defer r.slot.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
		defer cancel()
		r.run(ctx, jobID)
	}()
}

func (r *Runner) fail(job *store.ImportJob, err error) {
	logger.Error("IMPORT job=%d uid=%d failed: %v", job.ID, job.UserID, err)
	if e := r.Store.FinishImport(job.ID, "failed", trunc(err.Error(), 300)); e != nil {
		logger.Error("IMPORT job=%d could not record failure: %v", job.ID, e)
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func normName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c > 127 {
			b.WriteRune(c)
		}
	}
	return b.String()
}

func (r *Runner) resolve(ctx context.Context, s Show) (int64, string) {
	if s.TVDBID > 0 {
		item, err := r.TMDB.FindByExternal(ctx, strconv.FormatInt(s.TVDBID, 10), "tvdb_id")
		if err == nil {
			want := normName(s.Name)
			if want != "" && normName(item.Name) != want && normName(item.OriginalName) != want {
				if id, why := r.byName(ctx, s.Name); why == "" && id != 0 && id != item.ID {
					logger.Info("IMPORT %q: TMDB maps tvdb=%d to %q (tmdb=%d), using exact-name match tmdb=%d instead", s.Name, s.TVDBID, item.Name, item.ID, id)
					return id, ""
				}
			}
			return item.ID, ""
		}
		if !errors.Is(err, tmdb.ErrNotFound) {
			logger.Warn("IMPORT lookup tvdb=%d name=%q: %v", s.TVDBID, s.Name, err)
			return 0, "error"
		}
		if r.Sync != nil && r.Sync.TVDBConfigured() {
			return -s.TVDBID, ""
		}
	}
	return r.byName(ctx, s.Name)
}

func (r *Runner) byName(ctx context.Context, name string) (int64, string) {
	if strings.TrimSpace(name) == "" {
		return 0, "not_found"
	}
	s := Show{Name: name}
	res, err := r.TMDB.SearchTV(ctx, s.Name, 1, "en-US")
	if err != nil {
		logger.Warn("IMPORT search name=%q: %v", s.Name, err)
		return 0, "error"
	}
	want := normName(s.Name)
	var hit int64
	for _, it := range res.Results {
		if normName(it.Name) == want || normName(it.OriginalName) == want {
			if hit != 0 {
				return 0, "not_found"
			}
			hit = it.ID
		}
	}
	if hit == 0 {
		return 0, "not_found"
	}
	return hit, ""
}

const minWatchesToSwitch = 10

func coverage(index map[[2]int]int64, watches []Watch) float64 {
	if len(watches) == 0 {
		return 1
	}
	hit := 0
	for _, w := range watches {
		if _, ok := index[[2]int{w.Season, w.Episode}]; ok {
			hit++
		}
	}
	return float64(hit) / float64(len(watches))
}

const (
	maxEpisodeLookupsPerShow = 150
	maxEpisodeLookupsPerJob  = 3000
)

type jobState struct {
	*store.ImportJob
	lookups int
}

// byEpisodeID is the fallback for episodes whose season and number differ
// between TVDB and TMDB: the TVDB episode id is resolved to the TMDB episode.
func (r *Runner) byEpisodeID(ctx context.Context, tvdbEp int64, sh *store.Show, index map[[2]int]int64) (int64, bool) {
	f, err := r.TMDB.FindEpisodeByExternal(ctx, strconv.FormatInt(tvdbEp, 10), "tvdb_id")
	if err != nil {
		if !errors.Is(err, tmdb.ErrNotFound) {
			logger.Warn("IMPORT episode lookup tvdb=%d show=%d: %v", tvdbEp, sh.TMDBID, err)
		}
		return 0, false
	}
	if f.ShowID != sh.TMDBID {
		return 0, false
	}
	id, ok := index[[2]int{f.SeasonNumber, f.EpisodeNumber}]
	return id, ok
}

func statusFor(s Show, hasWatches bool) (status string, completedIfDone bool) {
	switch {
	case s.ForLater && !s.Followed:
		return "plan", false
	case s.Followed && s.Archived:
		return "paused", true
	case s.Followed:
		return "watching", false
	case s.Favorite:
		return "paused", true
	case hasWatches:
		return "dropped", true
	}
	return "", false
}

func (r *Runner) run(ctx context.Context, jobID int64) {
	job, payload, err := r.Store.ImportJobWithPayload(jobID)
	if err != nil {
		logger.Error("IMPORT job=%d load: %v", jobID, err)
		return
	}
	if job.Status != "pending" {
		return
	}
	var data Data
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		r.fail(job, fmt.Errorf("decode payload: %w", err))
		return
	}
	if !r.TMDB.Configured() {
		r.fail(job, errors.New("TMDB is not configured on this server"))
		return
	}
	if err := r.Store.StartImport(jobID); err != nil {
		r.fail(job, err)
		return
	}
	logger.Info("IMPORT job=%d uid=%d source=%s shows=%d watches=%d", job.ID, job.UserID, job.Source, len(data.Shows), len(data.Watches))

	byShow := map[int64][]Watch{}
	for _, w := range data.Watches {
		byShow[w.TVDBID] = append(byShow[w.TVDBID], w)
	}
	shows := append([]Show(nil), data.Shows...)
	sort.SliceStable(shows, func(i, j int) bool {
		a, b := shows[i], shows[j]
		if (a.Followed || a.ForLater) != (b.Followed || b.ForLater) {
			return a.Followed || a.ForLater
		}
		return len(byShow[a.TVDBID]) > len(byShow[b.TVDBID])
	})

	js := &jobState{ImportJob: job}
	saveEvery := 0
	for _, s := range shows {
		if ctx.Err() != nil {
			r.fail(job, fmt.Errorf("stopped: %w", ctx.Err()))
			return
		}
		watches := byShow[s.TVDBID]
		tmdbID, reason := r.resolve(ctx, s)
		var sh *store.Show
		if reason == "" {
			sh, err = r.Sync.EnsureShow(ctx, tmdbID)
			if err != nil && tmdbID < 0 && errors.Is(err, tvdb.ErrNotFound) {
				logger.Info("IMPORT %q is not on TheTVDB under id %d, trying TMDB by name", s.Name, -tmdbID)
				if id, why := r.byName(ctx, s.Name); id != 0 {
					tmdbID, reason = id, ""
					sh, err = r.Sync.EnsureShow(ctx, tmdbID)
				} else {
					reason = why
				}
			}
			if reason == "" && err != nil {
				logger.Warn("IMPORT sync show tmdb=%d name=%q: %v", tmdbID, s.Name, err)
				reason = "error"
			}
		}
		if reason != "" {
			job.Unmatched = append(job.Unmatched, store.UnmatchedShow{Name: s.Name, Reason: reason})
			job.EpisodesSkipped += len(watches)
		} else if err := r.importShow(ctx, js, s, sh, watches); err != nil {
			logger.Error("IMPORT job=%d show=%q: %v", job.ID, s.Name, err)
			job.Unmatched = append(job.Unmatched, store.UnmatchedShow{Name: s.Name, Reason: "error"})
			job.EpisodesSkipped += len(watches)
		}
		job.ShowsDone++
		if saveEvery++; saveEvery%3 == 0 || job.ShowsDone == len(shows) {
			if err := r.Store.SaveImportProgress(job); err != nil {
				logger.Error("IMPORT job=%d: %v", job.ID, err)
			}
		}
	}
	if err := r.Store.SaveImportProgress(job); err != nil {
		logger.Error("IMPORT job=%d: %v", job.ID, err)
	}
	if n, err := r.Store.CompleteFinished(job.UserID, 0); err != nil {
		logger.Error("IMPORT job=%d complete finished shows uid=%d: %v", job.ID, job.UserID, err)
	} else if n > 0 {
		logger.Info("IMPORT job=%d marked %d finished shows completed", job.ID, n)
	}
	if err := r.Store.FinishImport(job.ID, "done", ""); err != nil {
		logger.Error("IMPORT job=%d: %v", job.ID, err)
		return
	}
	logger.Info("IMPORT job=%d done shows=%d/%d episodes=%d skipped=%d follows=%d", job.ID, job.Matched, len(shows), job.EpisodesAdded, job.EpisodesSkipped, job.FollowsAdded)
	if r.Notify != nil {
		r.Notify.Send(job.UserID, "import_done", "/settings?tab=import", map[string]any{"shows": strconv.Itoa(job.Matched), "episodes": strconv.Itoa(job.EpisodesAdded)})
	}
	if r.Sync != nil {
		r.Sync.PrefetchLanguages()
	}
}

func (r *Runner) importShow(ctx context.Context, job *jobState, s Show, sh *store.Show, watches []Watch) error {
	index, err := r.Store.EpisodeIndex(sh.ID)
	if err != nil {
		return err
	}
	if sh.TMDBID > 0 && s.TVDBID > 0 && len(watches) >= minWatchesToSwitch && r.Sync != nil && r.Sync.TVDBConfigured() && coverage(index, watches) < 0.5 {
		if alt, err := r.Sync.EnsureShow(ctx, -s.TVDBID); err != nil {
			logger.Warn("IMPORT tvdb alternative for %q tvdb=%d: %v", s.Name, s.TVDBID, err)
		} else if altIndex, err := r.Store.EpisodeIndex(alt.ID); err != nil {
			logger.Warn("IMPORT tvdb alternative index for %q: %v", s.Name, err)
		} else if coverage(altIndex, watches) > coverage(index, watches) {
			logger.Info("IMPORT %q numbers better on TheTVDB (%.0f%% vs %.0f%%), using it", s.Name, coverage(altIndex, watches)*100, coverage(index, watches)*100)
			sh, index = alt, altIndex
		}
	}
	var eps []store.ImportedEpisode
	skipped := 0
	lookups := 0
	for _, w := range watches {
		id, ok := index[[2]int{w.Season, w.Episode}]
		if !ok && w.EpTVDB > 0 && sh.TMDBID > 0 && lookups < maxEpisodeLookupsPerShow && job.lookups < maxEpisodeLookupsPerJob {
			lookups++
			job.lookups++
			id, ok = r.byEpisodeID(ctx, w.EpTVDB, sh, index)
		}
		if !ok {
			skipped++
			continue
		}
		eps = append(eps, store.ImportedEpisode{EpisodeID: id, At: w.At})
	}
	status, done := statusFor(s, len(watches) > 0)
	res, err := r.Store.ImportShow(store.ImportShowInput{
		UserID: job.UserID, ShowID: sh.ID, Status: status, CompletedIfDone: done,
		Favorite: s.Favorite, FollowedAt: s.FollowedAt, Episodes: eps,
	})
	if err != nil {
		return err
	}
	job.Matched++
	job.EpisodesAdded += res.EpisodesAdded
	job.EpisodesSkipped += skipped
	if res.FollowAdded {
		job.FollowsAdded++
	}
	return nil
}
