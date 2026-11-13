package importer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"tvt/logger"
	"tvt/store"
	"tvt/youtube"
)

const SourceYouTube = "youtube"

type YTWatch struct {
	ID        string `json:"v"`
	Title     string `json:"t,omitempty"`
	Channel   string `json:"c,omitempty"`
	ChannelID string `json:"ch,omitempty"`
	At        string `json:"at,omitempty"`
}

type YTData struct {
	Videos []YTWatch `json:"videos"`
}

var (
	ErrNotYouTube = errors.New("not a YouTube watch history")
	channelURLRe  = regexp.MustCompile(`/channel/(UC[A-Za-z0-9_-]{22})`)
	watchURLRe    = regexp.MustCompile(`^https?://(?:www\.|m\.|music\.)?(?:youtube\.com/watch\?(?:[^#]*&)?v=|youtu\.be/)([A-Za-z0-9_-]{11})(?:[&#?].*)?$`)
)

type takeoutEntry struct {
	TitleURL  string `json:"titleUrl"`
	Time      string `json:"time"`
	Title     string `json:"title"`
	Subtitles []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"subtitles"`
}

// ParseYouTubeTakeout reads a Google Takeout watch history: the export zip or
// the JSON file inside it. Every watched video is kept once, with the first
// time it was watched. Entries that are not watched videos (searches, ads,
// community posts) are ignored.
func ParseYouTubeTakeout(r io.ReaderAt, size int64) (*YTData, error) {
	found := map[string]YTWatch{}
	var order []string
	read := func(rd io.Reader) error {
		_, err := readTakeoutJSON(rd, found, &order)
		return err
	}
	head := make([]byte, 4)
	if _, err := r.ReadAt(head, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %v", ErrNotYouTube, err)
	}
	if bytes.HasPrefix(head, []byte("PK")) {
		zr, err := zip.NewReader(r, size)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNotYouTube, err)
		}
		budget := int64(maxTotalBytes)
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(path.Base(f.Name)), ".json") {
				continue
			}
			if f.UncompressedSize64 > maxFileBytes || int64(f.UncompressedSize64) > budget {
				return nil, fmt.Errorf("%w: %s is too large", ErrNotYouTube, path.Base(f.Name))
			}
			budget -= int64(f.UncompressedSize64)
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrNotYouTube, err)
			}
			err = read(io.LimitReader(rc, maxFileBytes))
			rc.Close()
			if err != nil {
				logger.Warn("IMPORT youtube skipped %s: %v", path.Base(f.Name), err)
			}
		}
	} else if err := read(io.LimitReader(io.NewSectionReader(r, 0, size), maxFileBytes)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotYouTube, err)
	}
	if len(order) == 0 {
		return nil, ErrEmpty
	}
	prefix := commonTitlePrefix(found)
	out := &YTData{Videos: make([]YTWatch, 0, len(order))}
	for _, id := range order {
		w := found[id]
		w.Title = strings.TrimSpace(strings.TrimPrefix(w.Title, prefix))
		out.Videos = append(out.Videos, w)
	}
	return out, nil
}

// commonTitlePrefix is the words every entry's title starts with ("Watched "):
// Takeout writes it in the account's language, so it is found rather than
// assumed.
func commonTitlePrefix(found map[string]YTWatch) string {
	if len(found) < 5 {
		return "Watched "
	}
	prefix, first := "", true
	for _, w := range found {
		if first {
			prefix, first = w.Title, false
			continue
		}
		for !strings.HasPrefix(w.Title, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	if i := strings.LastIndex(prefix, " "); i >= 0 {
		return prefix[:i+1]
	}
	return ""
}

func readTakeoutJSON(rd io.Reader, found map[string]YTWatch, order *[]string) (int, error) {
	dec := json.NewDecoder(rd)
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return 0, errors.New("not a list of history entries")
	}
	n, kept := 0, 0
	for dec.More() {
		var e takeoutEntry
		if err := dec.Decode(&e); err != nil {
			return kept, err
		}
		if n++; n > maxRows {
			return kept, errors.New("too many entries")
		}
		m := watchURLRe.FindStringSubmatch(e.TitleURL)
		if m == nil {
			continue
		}
		id := m[1]
		at := normTime(e.Time)
		channel, channelID := "", ""
		if len(e.Subtitles) > 0 {
			channel = strings.TrimSpace(e.Subtitles[0].Name)
			if cm := channelURLRe.FindStringSubmatch(e.Subtitles[0].URL); cm != nil {
				channelID = cm[1]
			}
		}
		if prev, ok := found[id]; ok {
			if at != "" && (prev.At == "" || at < prev.At) {
				prev.At = at
				found[id] = prev
			}
			continue
		}
		found[id] = YTWatch{ID: id, Title: strings.TrimSpace(e.Title), Channel: channel, ChannelID: channelID, At: at}
		*order = append(*order, id)
		kept++
	}
	return kept, nil
}

func (r *Runner) runYouTube(job *store.ImportJob, payload string) {
	var data YTData
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		r.fail(job, fmt.Errorf("decode payload: %w", err))
		return
	}
	if err := r.Store.StartImport(job.ID); err != nil {
		r.fail(job, err)
		return
	}
	logger.Info("IMPORT job=%d uid=%d source=%s videos=%d", job.ID, job.UserID, job.Source, len(data.Videos))
	ids := make([]string, 0, len(data.Videos))
	for _, v := range data.Videos {
		if youtube.ValidVideoID(v.ID) {
			ids = append(ids, v.ID)
		}
	}
	if r.Sync == nil {
		r.fail(job, errors.New("youtube import: no syncer"))
		return
	}
	followed, err := r.Store.FollowedYouTubeChannels(job.UserID)
	if err != nil {
		r.fail(job, err)
		return
	}
	names := map[int64]string{}
	for _, c := range followed {
		names[c.ShowID] = c.Name
	}
	wanted := map[string]bool{}
	for _, v := range data.Videos {
		if _, ok := followed[v.ChannelID]; ok {
			wanted[v.ChannelID] = true
		}
	}
	job.Total = len(wanted)
	if err := r.Store.SaveImportProgress(job); err != nil {
		logger.Error("IMPORT job=%d: %v", job.ID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()
	backfilled := 0
	for source := range wanted {
		ch := followed[source]
		n, err := r.Sync.BackfillYouTubeChannel(ctx, ch.ShowID, source)
		job.ShowsDone++
		if err != nil {
			logger.Error("IMPORT job=%d youtube backfill show=%d %q: %v", job.ID, ch.ShowID, ch.Name, err)
			job.Unmatched = append(job.Unmatched, store.UnmatchedShow{Name: ch.Name, Reason: "error"})
			if e := r.Store.SaveImportProgress(job); e != nil {
				logger.Error("IMPORT job=%d: %v", job.ID, e)
			}
			continue
		}
		backfilled += n
		if e := r.Store.SaveImportProgress(job); e != nil {
			logger.Error("IMPORT job=%d: %v", job.ID, e)
		}
	}
	logger.Info("IMPORT job=%d youtube backfilled %d episodes across %d followed channels", job.ID, backfilled, len(wanted))
	targets, err := r.Store.YouTubeWatchTargets(job.UserID, ids)
	if err != nil {
		r.fail(job, err)
		return
	}
	byShow := map[int64][]store.ImportedEpisode{}
	matchedVideos := map[string]bool{}
	for _, v := range data.Videos {
		t, ok := targets[v.ID]
		if !ok || !t.Followed {
			continue
		}
		matchedVideos[v.ID] = true
		byShow[t.ShowID] = append(byShow[t.ShowID], store.ImportedEpisode{EpisodeID: t.EpisodeID, At: v.At})
	}
	for showID, eps := range byShow {
		res, err := r.Store.ImportShow(store.ImportShowInput{UserID: job.UserID, ShowID: showID, Episodes: eps})
		if err != nil {
			logger.Error("IMPORT job=%d youtube show=%d: %v", job.ID, showID, err)
			job.Unmatched = append(job.Unmatched, store.UnmatchedShow{Name: names[showID], Reason: "error"})
			job.EpisodesSkipped += len(eps)
			continue
		}
		job.Matched++
		job.EpisodesAdded += res.EpisodesAdded
	}
	counts := map[string]int{}
	for _, v := range data.Videos {
		if matchedVideos[v.ID] {
			continue
		}
		job.EpisodesSkipped++
		if _, isFollowed := followed[v.ChannelID]; isFollowed {
			continue
		}
		if v.Channel != "" {
			counts[v.Channel]++
		}
	}
	type chCount struct {
		name string
		n    int
	}
	var chans []chCount
	for name, n := range counts {
		chans = append(chans, chCount{name, n})
	}
	sort.Slice(chans, func(i, j int) bool {
		if chans[i].n != chans[j].n {
			return chans[i].n > chans[j].n
		}
		return chans[i].name < chans[j].name
	})
	for i, c := range chans {
		if i >= 100 {
			break
		}
		job.Unmatched = append(job.Unmatched, store.UnmatchedShow{Name: fmt.Sprintf("%s (%d)", c.name, c.n), Reason: "not_followed"})
	}
	if err := r.Store.SaveImportProgress(job); err != nil {
		logger.Error("IMPORT job=%d: %v", job.ID, err)
	}
	if err := r.Store.FinishImport(job.ID, "done", ""); err != nil {
		logger.Error("IMPORT job=%d: %v", job.ID, err)
		return
	}
	logger.Info("IMPORT job=%d youtube done shows=%d episodes=%d not_followed_videos=%d", job.ID, job.Matched, job.EpisodesAdded, job.EpisodesSkipped)
	if r.Notify != nil {
		r.Notify.Send(job.UserID, "import_done", "/settings?tab=import", map[string]any{"shows": fmt.Sprint(job.Matched), "episodes": fmt.Sprint(job.EpisodesAdded)})
	}
}
