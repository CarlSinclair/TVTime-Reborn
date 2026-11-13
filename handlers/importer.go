package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"tvt/auth"
	"tvt/importer"
	"tvt/logger"
	"tvt/store"
)

const maxImportUpload = 64 << 20

var (
	errImportInvalid        = apiErr{http.StatusBadRequest, "err.import_invalid"}
	errImportInvalidYouTube = apiErr{http.StatusBadRequest, "err.import_invalid_youtube"}
	errImportEmpty          = apiErr{http.StatusBadRequest, "err.import_empty"}
	errImportBusy           = apiErr{http.StatusConflict, "err.import_busy"}
	errImportTooBig         = apiErr{http.StatusRequestEntityTooLarge, "err.import_too_big"}
)

func (a *App) apiImportTVTime(w http.ResponseWriter, r *http.Request) {
	a.importUpload(w, r, importer.SourceTVTime)
}

func (a *App) apiImportYouTube(w http.ResponseWriter, r *http.Request) {
	a.importUpload(w, r, importer.SourceYouTube)
}

func (a *App) importUpload(w http.ResponseWriter, r *http.Request, source string) {
	u, err := a.needUser(r)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if source == importer.SourceTVTime && !a.TMDB.Configured() {
		a.fail(w, r, errTMDBMissing)
		return
	}
	if err := a.allow(a.mailLimit, "imp:"+itoa(u.ID)); err != nil {
		a.fail(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportUpload+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			a.fail(w, r, errImportTooBig)
			return
		}
		logger.Warn("IMPORT upload uid=%d unreadable: %v", u.ID, err)
		a.fail(w, r, errImportInvalid)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		a.fail(w, r, errImportInvalid)
		return
	}
	defer file.Close()
	if header.Size > maxImportUpload {
		a.fail(w, r, errImportTooBig)
		return
	}
	var payload []byte
	var shows int
	if source == importer.SourceYouTube {
		data, err := importer.ParseYouTubeTakeout(file, header.Size)
		if err != nil {
			a.importParseFailed(w, r, u.ID, source, err)
			return
		}
		if payload, err = json.Marshal(data); err != nil {
			a.fail(w, r, err)
			return
		}
		shows = len(data.Videos)
	} else {
		data, err := importer.ParseTVTime(file, header.Size)
		if err != nil {
			a.importParseFailed(w, r, u.ID, source, err)
			return
		}
		if payload, err = json.Marshal(data); err != nil {
			a.fail(w, r, err)
			return
		}
		shows = len(data.Shows)
	}
	id, err := a.Store.CreateImportJob(u.ID, source, string(payload), shows)
	if errors.Is(err, store.ErrImportBusy) {
		a.fail(w, r, errImportBusy)
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	logger.Info("IMPORT queued job=%d uid=%d source=%s items=%d", id, u.ID, source, shows)
	a.Importer.Start(id)
	a.okJSON(w, map[string]any{"id": id})
}

func (a *App) importParseFailed(w http.ResponseWriter, r *http.Request, userID int64, source string, err error) {
	switch {
	case errors.Is(err, importer.ErrEmpty):
		a.fail(w, r, errImportEmpty)
	case errors.Is(err, importer.ErrNotExport):
		logger.Warn("IMPORT upload uid=%d rejected: %v", userID, err)
		a.fail(w, r, errImportInvalid)
	case errors.Is(err, importer.ErrNotYouTube):
		logger.Warn("IMPORT upload uid=%d source=%s rejected: %v", userID, source, err)
		a.fail(w, r, errImportInvalidYouTube)
	default:
		a.fail(w, r, err)
	}
}

type importStatus struct {
	ID              int64                 `json:"id"`
	Status          string                `json:"status"`
	Total           int                   `json:"total"`
	ShowsDone       int                   `json:"shows_done"`
	Matched         int                   `json:"matched"`
	EpisodesAdded   int                   `json:"episodes_added"`
	EpisodesSkipped int                   `json:"episodes_skipped"`
	FollowsAdded    int                   `json:"follows_added"`
	Unmatched       []store.UnmatchedShow `json:"unmatched"`
	Active          bool                  `json:"active"`
}

func importView(j *store.ImportJob) *importStatus {
	if j == nil {
		return nil
	}
	un := j.Unmatched
	if len(un) > 300 {
		un = un[:300]
	}
	return &importStatus{ID: j.ID, Status: j.Status, Total: j.Total, ShowsDone: j.ShowsDone, Matched: j.Matched, EpisodesAdded: j.EpisodesAdded,
		EpisodesSkipped: j.EpisodesSkipped, FollowsAdded: j.FollowsAdded, Unmatched: un, Active: j.Active()}
}

func (a *App) apiImportStatus(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if u == nil {
		a.fail(w, r, errUnauth)
		return
	}
	j, err := a.Store.LatestImport(u.ID)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.okJSON(w, map[string]any{"job": importView(j)})
}
