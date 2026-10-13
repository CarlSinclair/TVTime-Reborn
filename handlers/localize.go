package handlers

import (
	"tvt/logger"
	"tvt/store"
)

func (a *App) localizeUpNext(lang string, items []store.UpNextItem) {
	if lang == "en" || len(items) == 0 {
		return
	}
	shows := make([]*store.Show, len(items))
	eps := make([]*store.Episode, len(items))
	for i := range items {
		shows[i], eps[i] = &items[i].Show, &items[i].Episode
	}
	if err := a.Store.LocalizeShows(lang, shows...); err != nil {
		logger.Error("LOCALIZE up next shows lang=%s: %v", lang, err)
	}
	if err := a.Store.LocalizeEpisodes(lang, eps...); err != nil {
		logger.Error("LOCALIZE up next episodes lang=%s: %v", lang, err)
	}
}

func (a *App) localizeCalendar(lang string, items []store.CalendarItem) {
	if lang == "en" || len(items) == 0 {
		return
	}
	eps := make([]*store.Episode, len(items))
	ids := make([]int64, len(items))
	for i := range items {
		eps[i] = &items[i].Episode
		ids[i] = items[i].ShowID
	}
	if err := a.Store.LocalizeEpisodes(lang, eps...); err != nil {
		logger.Error("LOCALIZE calendar episodes lang=%s: %v", lang, err)
	}
	texts, err := a.Store.ShowTexts(lang, ids)
	if err != nil {
		logger.Error("LOCALIZE calendar shows lang=%s: %v", lang, err)
		return
	}
	for i := range items {
		if t, ok := texts[items[i].ShowID]; ok && t.Name != "" {
			items[i].ShowName = t.Name
		}
	}
}

func (a *App) localizeEntries(lang string, entries []store.ShowEntry) {
	if lang == "en" || len(entries) == 0 {
		return
	}
	shows := make([]*store.Show, len(entries))
	for i := range entries {
		shows[i] = &entries[i].Show
	}
	if err := a.Store.LocalizeShows(lang, shows...); err != nil {
		logger.Error("LOCALIZE entries lang=%s: %v", lang, err)
	}
}

func (a *App) localizeShowList(lang string, list []store.Show) {
	if lang == "en" || len(list) == 0 {
		return
	}
	ptrs := make([]*store.Show, len(list))
	for i := range list {
		ptrs[i] = &list[i]
	}
	if err := a.Store.LocalizeShows(lang, ptrs...); err != nil {
		logger.Error("LOCALIZE show list lang=%s: %v", lang, err)
	}
}
