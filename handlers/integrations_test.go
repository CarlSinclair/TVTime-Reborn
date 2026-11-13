package handlers

import "testing"

func TestParseEpisodeMark(t *testing.T) {
	cases := []struct {
		fields          []string
		season, episode int
		first           string
		ok              bool
	}{
		{[]string{"Severance", `S2:E5 "Trojan's Horse"`}, 2, 5, "Severance", true},
		{[]string{"Severance - S02E05 - Trojans Horse"}, 2, 5, "Severance", true},
		{[]string{"Episode title", "Season 1, Episode 3", "Some Show"}, 1, 3, "Episode title", true},
		{[]string{"Show Name 3x07"}, 3, 7, "Show Name", true},
		{[]string{"A Movie", "2024"}, 0, 0, "", false},
		{[]string{"Song 4x4 beat"}, 4, 4, "Song", true},
		{[]string{"Show", "S1E0"}, 0, 0, "", false},
	}
	for _, c := range cases {
		s, e, cand, ok := parseEpisodeMark(c.fields...)
		if ok != c.ok || s != c.season || e != c.episode {
			t.Errorf("%v: got s%d e%d ok=%v", c.fields, s, e, ok)
			continue
		}
		if ok && (len(cand) == 0 || cand[0] != c.first) {
			t.Errorf("%v: candidates %v, want first %q", c.fields, cand, c.first)
		}
	}
}
