package youtube

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIPlaylistVideosPagesAndDates(t *testing.T) {
	var sawKeyHeader, sawKeyInURL bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Goog-Api-Key") == "secret-key" {
			sawKeyHeader = true
		}
		if strings.Contains(r.URL.String(), "secret-key") {
			sawKeyInURL = true
		}
		if r.URL.Query().Get("pageToken") == "" {
			w.Write([]byte(`{"nextPageToken":"p2","items":[
				{"snippet":{"title":"One"},"contentDetails":{"videoId":"AAAAAAAAAAA","videoPublishedAt":"2024-03-01T10:00:00Z"}},
				{"snippet":{"title":"Private video"},"contentDetails":{"videoId":"BBBBBBBBBBB"}}]}`))
			return
		}
		w.Write([]byte(`{"items":[{"snippet":{"title":"Two"},"contentDetails":{"videoId":"CCCCCCCCCCC","videoPublishedAt":"2023-01-02T03:04:05Z"}}]}`))
	}))
	defer srv.Close()
	api := NewAPI("secret-key")
	api.Base = srv.URL
	entries, dates, err := api.PlaylistVideos(context.Background(), "UUabcdefghijklmnopqrstuv")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || len(dates) != 2 {
		t.Fatalf("3 entries, 2 dated expected, got %d and %d", len(entries), len(dates))
	}
	if _, ok := dates["BBBBBBBBBBB"]; ok {
		t.Fatalf("an undated video must not get a date")
	}
	if dates["CCCCCCCCCCC"].Year() != 2023 || dates["AAAAAAAAAAA"].Month() != 3 {
		t.Fatalf("dates parse: %v", dates)
	}
	if !sawKeyHeader || sawKeyInURL {
		t.Fatalf("the key goes in the header only: header=%v url=%v", sawKeyHeader, sawKeyInURL)
	}
}

func TestAPIErrorsAreTypedAndNeverLeakTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"quota","errors":[{"reason":"quotaExceeded"}]}}`))
	}))
	defer srv.Close()
	api := NewAPI("secret-key")
	api.Base = srv.URL
	_, _, err := api.PlaylistVideos(context.Background(), "UUabcdefghijklmnopqrstuv")
	if err == nil || !IsAPIQuota(err) {
		t.Fatalf("quota errors are recognised: %v", err)
	}
	if strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("the error must not contain the key: %v", err)
	}
	if _, _, err := api.PlaylistVideos(context.Background(), "not-a-playlist"); err == nil {
		t.Fatalf("a bad playlist id is refused before any request")
	}
	if _, _, err := NewAPI("").PlaylistVideos(context.Background(), "UUabcdefghijklmnopqrstuv"); err == nil {
		t.Fatalf("no key, no request")
	}
}
