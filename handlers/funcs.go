package handlers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func imgURL(size, path string) string {
	if path == "" {
		return "/static/img/placeholder.svg"
	}
	return "/img/" + size + path
}

func episodeCode(season, episode int) string {
	return fmt.Sprintf("S%02dE%02d", season, episode)
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func pct(part, total int) int {
	if total <= 0 {
		return 0
	}
	return part * 100 / total
}

func year(d string) string {
	if len(d) >= 4 {
		return d[:4]
	}
	return ""
}

func aired(d string) bool {
	return d != "" && d <= time.Now().UTC().Format("2006-01-02")
}

func dict(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	return m
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n])) + "..."
}

func fmtMinutes(m int) string {
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	if m < 60*24 {
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dd %dh", m/(60*24), (m/60)%24)
}

func initial(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
