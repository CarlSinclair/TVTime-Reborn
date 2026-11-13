package notify

import (
	"encoding/base64"
	"fmt"
	"html"
	"strings"
)

// translateHTML renders an i18n string for Telegram's HTML parse mode: the
// translation text and every value are escaped, then placeholders are filled.
func (s *Service) translateHTML(lang, key string, data map[string]any, repl map[string]string) string {
	pairs := make([]string, 0, len(data)*2)
	for k, v := range data {
		if rv, ok := repl[k]; ok {
			pairs = append(pairs, "{"+k+"}", rv)
			continue
		}
		pairs = append(pairs, "{"+k+"}", html.EscapeString(fmt.Sprint(v)))
	}
	return strings.NewReplacer(pairs...).Replace(html.EscapeString(s.I18n.T(lang, key)))
}

// telegramHTML builds the Telegram message: a bold title and the body. The link
// sits on the episode code and name when the body names the episode, otherwise
// on the title, so no bare URL is shown.
func (s *Service) telegramHTML(lang, event string, data map[string]any, absLink string) string {
	var repl map[string]string
	titleLinked := absLink != ""
	if event == "episode_aired" && absLink != "" {
		open := `<a href="` + html.EscapeString(absLink) + `">`
		repl = map[string]string{
			"code": open + html.EscapeString(fmt.Sprint(data["code"])),
			"name": html.EscapeString(fmt.Sprint(data["name"])) + "</a>",
		}
		titleLinked = false
	}
	title := s.translateHTML(lang, "notif."+event+".title", data, nil)
	if titleLinked {
		title = `<a href="` + html.EscapeString(absLink) + `">` + title + `</a>`
	}
	msg := "<b>" + title + "</b>"
	if body := s.translateHTML(lang, "notif."+event+".body", data, repl); strings.TrimSpace(body) != "" {
		msg += "\n" + body
	}
	return msg
}

// artworkPath maps a stored TMDB or TheTVDB image path to the site's own image
// route, or "" when there is none.
func artworkPath(path string) string {
	switch {
	case path == "":
		return ""
	case strings.HasPrefix(path, "yt:"):
		return "/ytimg/v/" + strings.TrimPrefix(path, "yt:")
	case strings.HasPrefix(path, "ytc:"):
		return "/ytimg/c/" + strings.TrimPrefix(path, "ytc:")
	case strings.HasPrefix(path, "tvdb:"):
		return "/tvdbimg/" + base64.RawURLEncoding.EncodeToString([]byte(strings.TrimPrefix(path, "tvdb:")))
	}
	return "/img/w780" + path
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
