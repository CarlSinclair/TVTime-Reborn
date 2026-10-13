package importer

import (
	"archive/zip"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	maxFileBytes  = 128 << 20
	maxTotalBytes = 256 << 20
	maxRows       = 2_000_000
)

var (
	ErrNotExport = errors.New("not a TV Time export")
	ErrEmpty     = errors.New("export has no shows or watched episodes")
)

type Show struct {
	TVDBID     int64  `json:"tvdb"`
	Name       string `json:"name"`
	Followed   bool   `json:"followed,omitempty"`
	ForLater   bool   `json:"for_later,omitempty"`
	Archived   bool   `json:"archived,omitempty"`
	Favorite   bool   `json:"favorite,omitempty"`
	FollowedAt string `json:"followed_at,omitempty"`
}

type Watch struct {
	TVDBID  int64  `json:"tvdb"`
	Season  int    `json:"s"`
	Episode int    `json:"e"`
	At      string `json:"at,omitempty"`
	EpTVDB  int64  `json:"ep,omitempty"`
}

type Data struct {
	Shows   []Show  `json:"shows"`
	Watches []Watch `json:"watches"`
}

type zipFiles map[string]*zip.File

func openZip(r io.ReaderAt, size int64) (zipFiles, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotExport, err)
	}
	files := zipFiles{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		files[strings.ToLower(path.Base(f.Name))] = f
	}
	return files, nil
}

type rows struct {
	header map[string]int
	r      *csv.Reader
	rc     io.ReadCloser
	rec    []string
}

func (z zipFiles) csv(name string, budget *int64) (*rows, error) {
	f, ok := z[name]
	if !ok {
		return nil, nil
	}
	if f.UncompressedSize64 > maxFileBytes {
		return nil, fmt.Errorf("%w: %s is too large", ErrNotExport, name)
	}
	*budget -= int64(f.UncompressedSize64)
	if *budget < 0 {
		return nil, fmt.Errorf("%w: archive is too large", ErrNotExport)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	cr := csv.NewReader(io.LimitReader(rc, maxFileBytes))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	head, err := cr.Read()
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("%w: %s has no header: %v", ErrNotExport, name, err)
	}
	h := map[string]int{}
	for i, c := range head {
		h[strings.TrimPrefix(strings.TrimSpace(c), "\xef\xbb\xbf")] = i
	}
	return &rows{header: h, r: cr, rc: rc}, nil
}

func (r *rows) next() bool {
	rec, err := r.r.Read()
	if err != nil {
		return false
	}
	r.rec = rec
	return true
}

func (r *rows) get(col string) string {
	i, ok := r.header[col]
	if !ok || i >= len(r.rec) {
		return ""
	}
	return strings.TrimSpace(r.rec[i])
}

func (r *rows) close() {
	if r != nil {
		r.rc.Close()
	}
}

func atoi64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func normTime(s string) string {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format("2006-01-02 15:04:05")
		}
	}
	return ""
}

// ParseTVTime reads the GDPR export zip from TV Time. Only the few CSV files
// the import needs are opened, entirely in memory; nothing is extracted to disk.
func ParseTVTime(r io.ReaderAt, size int64) (*Data, error) {
	files, err := openZip(r, size)
	if err != nil {
		return nil, err
	}
	budget := int64(maxTotalBytes)
	shows := map[int64]*Show{}
	show := func(id int64, name string) *Show {
		s, ok := shows[id]
		if !ok {
			s = &Show{TVDBID: id}
			shows[id] = s
		}
		if s.Name == "" {
			s.Name = name
		}
		return s
	}
	type wkey struct {
		id   int64
		s, e int
	}
	type wval struct {
		at string
		ep int64
	}
	watches := map[wkey]wval{}

	track, err := files.csv("tracking-prod-records-v2.csv", &budget)
	if err != nil {
		return nil, err
	}
	if track != nil {
		defer track.close()
		n := 0
		for track.next() {
			if n++; n > maxRows {
				return nil, fmt.Errorf("%w: too many rows", ErrNotExport)
			}
			key := track.get("key")
			switch {
			case strings.HasPrefix(key, "watch-episode-"), strings.HasPrefix(key, "rewatch-episode-"):
				id := atoi64(track.get("s_id"))
				season, e1 := strconv.Atoi(firstNonEmpty(track.get("season_number"), track.get("s_no")))
				ep, e2 := strconv.Atoi(firstNonEmpty(track.get("episode_number"), track.get("ep_no")))
				if id == 0 || e1 != nil || e2 != nil {
					continue
				}
				show(id, track.get("series_name"))
				k := wkey{id, season, ep}
				at := normTime(track.get("created_at"))
				epID := atoi64(firstNonEmpty(track.get("episode_id"), track.get("ep_id")))
				if old, seen := watches[k]; !seen || (at != "" && (old.at == "" || at < old.at)) {
					watches[k] = wval{at, epID}
				}
			case strings.HasPrefix(key, "user-series-"):
				id := atoi64(track.get("s_id"))
				if id == 0 {
					continue
				}
				s := show(id, track.get("series_name"))
				s.Followed = truthy(track.get("is_followed"))
				s.ForLater = truthy(track.get("is_for_later"))
				s.Archived = truthy(track.get("is_archived"))
				s.FollowedAt = normTime(firstNonEmpty(track.get("followed_at"), track.get("created_at")))
			}
		}
	}

	if st, err := files.csv("user_show_special_status.csv", &budget); err != nil {
		return nil, err
	} else if st != nil {
		for st.next() {
			id := atoi64(st.get("tv_show_id"))
			if id == 0 {
				continue
			}
			s := show(id, st.get("tv_show_name"))
			switch st.get("status") {
			case "favorite":
				s.Favorite = true
			case "for_later":
				s.ForLater = true
			}
		}
		st.close()
	}
	if ud, err := files.csv("user_tv_show_data.csv", &budget); err != nil {
		return nil, err
	} else if ud != nil {
		for ud.next() {
			id := atoi64(ud.get("tv_show_id"))
			if id == 0 {
				continue
			}
			s := show(id, ud.get("tv_show_name"))
			if truthy(ud.get("is_favorited")) {
				s.Favorite = true
			}
			if truthy(ud.get("is_followed")) && track == nil {
				s.Followed = true
			}
		}
		ud.close()
	}

	if track == nil && len(shows) == 0 {
		return nil, ErrNotExport
	}
	if len(shows) == 0 {
		return nil, ErrEmpty
	}
	d := &Data{}
	for _, s := range shows {
		d.Shows = append(d.Shows, *s)
	}
	sort.Slice(d.Shows, func(i, j int) bool { return d.Shows[i].TVDBID < d.Shows[j].TVDBID })
	for k, v := range watches {
		d.Watches = append(d.Watches, Watch{TVDBID: k.id, Season: k.s, Episode: k.e, At: v.at, EpTVDB: v.ep})
	}
	sort.Slice(d.Watches, func(i, j int) bool {
		a, b := d.Watches[i], d.Watches[j]
		if a.TVDBID != b.TVDBID {
			return a.TVDBID < b.TVDBID
		}
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		return a.Episode < b.Episode
	})
	return d, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
