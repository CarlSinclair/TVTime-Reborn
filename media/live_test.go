package media

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveS3 is an opt-in check against a real bucket. It runs only when
// TVT_LIVE_S3=1 and the TVT_S3_* variables are set, e.g.
//
//	set -a; . ./tvt.env; set +a; TVT_LIVE_S3=1 go test -run TestLiveS3 ./media
//
// It writes, reads back and deletes one small object and prints no secrets.
func TestLiveS3(t *testing.T) {
	if os.Getenv("TVT_LIVE_S3") != "1" {
		t.Skip("set TVT_LIVE_S3=1 to run against a real bucket")
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	cfg := S3Config{
		Endpoint: get("TVT_S3_ENDPOINT"), Region: get("TVT_S3_REGION"),
		Bucket: get("TVT_S3_BUCKET"), AccessKeyID: get("TVT_S3_ACCESS_KEY_ID"),
		SecretAccessKey: get("TVT_S3_SECRET_ACCESS_KEY"), Prefix: get("TVT_S3_PREFIX"),
	}
	s3c, err := NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if cfg.Bucket == "" {
		t.Skip("no bucket configured")
	}
	key := "livecheck/" + time.Now().UTC().Format("20060102-150405") + ".png"
	if err := s3c.Put(ctx, key, []byte("tvt live check"), "image/png"); err != nil {
		t.Fatalf("put: %v", err)
	}
	o, err := s3c.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	b, _ := io.ReadAll(o.Body)
	o.Body.Close()
	if string(b) != "tvt live check" {
		t.Fatalf("read back %q", b)
	}
	if pub := strings.TrimRight(get("TVT_MEDIA_PUBLIC_URL"), "/"); pub != "" {
		resp, err := http.Get(pub + "/" + cfg.Prefix + key)
		if err != nil {
			t.Fatalf("public url: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Logf("public url %s -> %d, content-type %q, cache-control %q, body ok=%v", pub, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"), string(body) == "tvt live check")
	}
	if err := s3c.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s3c.Get(ctx, key); err == nil {
		t.Fatalf("object still readable after delete")
	}
	t.Logf("live s3 round trip ok (bucket %q)", cfg.Bucket)
}
