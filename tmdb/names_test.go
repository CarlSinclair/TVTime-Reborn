package tmdb

import "testing"

func TestDisplayNameReplacesNonLatinTitle(t *testing.T) {
	alt := func(country, title, typ string) AltTitle { return AltTitle{Country: country, Title: title, Type: typ} }
	cases := []struct {
		name string
		d    ShowDetails
		want string
	}{
		{"latin kept", ShowDetails{Name: "Breaking Bad"}, "Breaking Bad"},
		{"no alternative keeps original", ShowDetails{Name: "進撃の巨人"}, "進撃の巨人"},
		{"prefers an english region", ShowDetails{Name: "進撃の巨人", AltTitles: struct {
			Results []AltTitle `json:"results"`
		}{[]AltTitle{alt("DE", "Angriff der Titanen", ""), alt("", "Shingeki no Kyojin", "Romaji"), alt("US", "Attack on Titan", "")}}}, "Attack on Titan"},
		{"falls back to romaji", ShowDetails{Name: "進撃の巨人", AltTitles: struct {
			Results []AltTitle `json:"results"`
		}{[]AltTitle{alt("JP", "進撃の巨人", ""), alt("", "Shingeki no Kyojin", "Romaji")}}}, "Shingeki no Kyojin"},
	}
	for _, c := range cases {
		if got := c.d.DisplayName(); got != c.want {
			t.Errorf("%s: DisplayName() = %q, want %q", c.name, got, c.want)
		}
	}
}
