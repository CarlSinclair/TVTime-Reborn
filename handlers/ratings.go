package handlers

import (
	"fmt"
	"html/template"
	"strings"

	"tvt/store"
)

type ratingChart struct {
	SVG        template.HTML
	HasMembers bool
	HasTMDB    bool
}

const (
	chartW = 720.0
	chartH = 240.0
	chartL = 34.0
	chartR = 12.0
	chartT = 12.0
	chartB = 26.0
)

// buildRatingChart draws one point per episode for each source on a 0 to 10
// scale. A source with nothing to show is left out; with neither there is no
// chart.
func buildRatingChart(list []store.EpisodeRating) *ratingChart {
	return buildChart(list, 0, 0, false)
}

// seasonRatingChart is the chart of one season, with each episode labelled.
func seasonRatingChart(all []store.EpisodeRating, season int) *ratingChart {
	return buildChart(seasonOf(all, season), 0, 0, true)
}

// episodeRatingChart is the chart of an episode's season with the episode marked.
func episodeRatingChart(all []store.EpisodeRating, season, episode int) *ratingChart {
	return buildChart(seasonOf(all, season), season, episode, true)
}

func seasonOf(all []store.EpisodeRating, season int) []store.EpisodeRating {
	var out []store.EpisodeRating
	for _, r := range all {
		if r.Season == season {
			out = append(out, r)
		}
	}
	return out
}

func buildChart(list []store.EpisodeRating, markSeason, markEpisode int, episodeLabels bool) *ratingChart {
	n := len(list)
	if n == 0 {
		return nil
	}
	c := &ratingChart{}
	for _, r := range list {
		if r.MemberVotes > 0 {
			c.HasMembers = true
		}
		if r.TMDBVotes > 0 && r.TMDBAvg > 0 {
			c.HasTMDB = true
		}
	}
	if !c.HasMembers && !c.HasTMDB {
		return nil
	}
	x := func(i int) float64 {
		if n == 1 {
			return chartL + (chartW-chartL-chartR)/2
		}
		return chartL + (chartW-chartL-chartR)*float64(i)/float64(n-1)
	}
	y := func(v float64) float64 { return chartT + (chartH-chartT-chartB)*(1-v/10) }
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %.0f %.0f" role="img">`, chartW, chartH)
	for v := 0; v <= 10; v += 2 {
		fmt.Fprintf(&b, `<line class="grid" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><text class="axis" x="%.1f" y="%.1f" text-anchor="end">%d</text>`,
			chartL, y(float64(v)), chartW-chartR, y(float64(v)), chartL-6, y(float64(v))+4, v)
	}
	last := 0
	if episodeLabels {
		step := 1
		if n > 14 {
			step = (n + 13) / 14
		}
		for i, r := range list {
			if i%step == 0 || i == n-1 {
				fmt.Fprintf(&b, `<text class="axis" x="%.1f" y="%.1f" text-anchor="middle">%d</text>`, x(i), chartH-8, r.Episode)
			}
		}
	} else {
		for i, r := range list {
			if r.Season != last {
				last = r.Season
				fmt.Fprintf(&b, `<line class="season" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/><text class="axis" x="%.1f" y="%.1f">S%d</text>`,
					x(i), chartT, x(i), chartH-chartB, x(i)+3, chartH-8, r.Season)
			}
		}
	}
	line := func(class string, ok func(store.EpisodeRating) bool, val func(store.EpisodeRating) float64) {
		var d strings.Builder
		pen := false
		for i, r := range list {
			if !ok(r) {
				pen = false
				continue
			}
			cmd := "L"
			if !pen {
				cmd = "M"
				pen = true
			}
			fmt.Fprintf(&d, "%s%.1f,%.1f ", cmd, x(i), y(val(r)))
		}
		if d.Len() > 0 {
			fmt.Fprintf(&b, `<path class="line %s" d="%s"/>`, class, strings.TrimSpace(d.String()))
		}
	}
	if c.HasTMDB {
		line("tmdb", func(r store.EpisodeRating) bool { return r.TMDBVotes > 0 && r.TMDBAvg > 0 }, func(r store.EpisodeRating) float64 { return r.TMDBAvg })
	}
	if c.HasMembers {
		line("members", func(r store.EpisodeRating) bool { return r.MemberVotes > 0 }, func(r store.EpisodeRating) float64 { return r.MemberAvg })
	}
	dot := func(class string, ok bool, val float64, i int) {
		if ok {
			fmt.Fprintf(&b, `<circle class="dot %s" cx="%.1f" cy="%.1f" r="%d"/>`, class, x(i), y(val), 3)
		}
	}
	for i, r := range list {
		if episodeLabels || n <= 40 {
			dot("tmdb", c.HasTMDB && r.TMDBVotes > 0 && r.TMDBAvg > 0, r.TMDBAvg, i)
			dot("members", c.HasMembers && r.MemberVotes > 0, r.MemberAvg, i)
		}
		if markEpisode > 0 && r.Season == markSeason && r.Episode == markEpisode {
			fmt.Fprintf(&b, `<line class="mark" x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f"/>`, x(i), chartT, x(i), chartH-chartB)
		}
	}
	b.WriteString(`</svg>`)
	c.SVG = template.HTML(b.String())
	return c
}

// starsOf converts a stored 1 to 10 rating into whole stars.
func starsOf(rating int) int { return (rating + 1) / 2 }

type starRow struct{ Stars, Count int }

// starRows lists whole-star counts from five stars down, with their total.
func starRows(counts [5]int) ([]starRow, int) {
	var rows []starRow
	total := 0
	for i := 4; i >= 0; i-- {
		rows = append(rows, starRow{Stars: i + 1, Count: counts[i]})
		total += counts[i]
	}
	return rows, total
}
