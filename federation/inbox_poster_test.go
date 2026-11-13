package federation

import "testing"

func TestSanitizePosterYouTube(t *testing.T) {
	good := []string{"yt:FsEbLPqeo7k", "ytc:eXQzLmdncGh0LmNvbS9hYmNkZWY"}
	for _, p := range good {
		if sanitizePoster(p) != p {
			t.Errorf("%q should be kept", p)
		}
	}
	for _, p := range []string{"yt:short", "yt:../../etc/passwd", "ytc:bm90LWEtaG9zdA", "ytc:!!!"} {
		if sanitizePoster(p) != "" {
			t.Errorf("%q should be dropped", p)
		}
	}
}
