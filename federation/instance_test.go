package federation

import "testing"

func TestCleanInstanceName(t *testing.T) {
	good := map[string]string{"TVT": "TVT", "  Carl's   TVT ": "Carl's TVT", "Séries & Co.": "Séries & Co.", "电视 TV": "电视 TV", "Fun (beta)": "Fun (beta)"}
	for in, want := range good {
		if got, err := CleanInstanceName(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "a", "-TVT", "TVT-", "tv<script>", "a\nb\x00", "x https://evil", "😀😀", string(make([]rune, 41))} {
		if _, err := CleanInstanceName(in); err == nil {
			t.Errorf("%q should be refused", in)
		}
	}
}
