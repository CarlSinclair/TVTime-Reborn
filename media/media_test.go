package media

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestValidKey(t *testing.T) {
	for _, ok := range []string{"avatars/1-abcd.jpg", "tmdb/w342/abc_DEF-1.jpg"[:0] + "tmdb/w342/abc.jpg"} {
		if !ValidKey(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "../etc/passwd", "a/../b", "/abs", "a//b", "A/upper", "a/b/", ".hidden", "a/./b", strings.Repeat("a", 300)} {
		if ValidKey(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestDiskRoundTripAndTraversal(t *testing.T) {
	d := &Disk{Dir: t.TempDir()}
	ctx := context.Background()
	if _, err := d.Get(ctx, "avatars/x.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	if err := d.Put(ctx, "avatars/x.jpg", []byte("hello"), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	o, err := d.Get(ctx, "avatars/x.jpg")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(o.Body)
	o.Body.Close()
	if string(b) != "hello" || o.Size != 5 || o.ContentType != "image/jpeg" {
		t.Fatalf("got %q size=%d ct=%s", b, o.Size, o.ContentType)
	}
	if err := d.Put(ctx, "../escape.jpg", []byte("x"), "image/jpeg"); err == nil {
		t.Fatalf("traversal key accepted")
	}
	if err := d.Delete(ctx, "avatars/x.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, "avatars/x.jpg"); err != nil {
		t.Fatalf("deleting a missing object should be a no-op: %v", err)
	}
}

func TestS3ConfigValidation(t *testing.T) {
	if (S3Config{}).Configured() || (S3Config{}).Partial() {
		t.Fatalf("empty config is neither configured nor partial")
	}
	if !(S3Config{Bucket: "b"}).Partial() {
		t.Fatalf("a lone bucket must be flagged as a misconfiguration")
	}
	full := S3Config{Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}
	if !full.Configured() || full.Partial() {
		t.Fatalf("full config")
	}
}

// A minimal in-memory S3 endpoint is enough to prove the R2 backend signs and
// issues PUT/GET/DELETE against the right bucket/prefix path.
func TestS3AgainstFakeServer(t *testing.T) {
	var mu sync.Mutex
	objs := map[string][]byte{}
	var auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
				b = decodeAWSChunked(b)
			}
			objs[r.URL.Path] = b
			w.WriteHeader(http.StatusOK)
		case http.MethodGet, http.MethodHead:
			b, ok := objs[r.URL.Path]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Length", "3")
			w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
			w.Header().Set("ETag", `"abc123"`)
			if rg := r.Header.Get("Range"); rg != "" {
				w.Header().Set("Content-Range", "bytes 0-2/3")
				w.WriteHeader(http.StatusPartialContent)
			} else {
				w.WriteHeader(http.StatusOK)
			}
			if r.Method == http.MethodGet {
				w.Write(b)
			}
		case http.MethodDelete:
			delete(objs, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	s3c, err := NewS3(S3Config{Endpoint: srv.URL, Bucket: "bkt", AccessKeyID: "AK", SecretAccessKey: "SK", Prefix: "tvt"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s3c.Get(ctx, "tmdb/w92/a.png"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := s3c.Put(ctx, "tmdb/w92/a.png", []byte("png"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if _, ok := objs["/bkt/tvt/tmdb/w92/a.png"]; !ok {
		t.Fatalf("object not stored under bucket/prefix: %v", keys(objs))
	}
	o, err := s3c.Get(ctx, "tmdb/w92/a.png")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(o.Body)
	o.Body.Close()
	if string(b) != "png" || o.ContentType != "image/png" {
		t.Fatalf("got %q %s", b, o.ContentType)
	}
	if err := s3c.Delete(ctx, "tmdb/w92/a.png"); err != nil {
		t.Fatal(err)
	}
	for _, a := range auth {
		if !strings.HasPrefix(a, "AWS4-HMAC-SHA256") {
			t.Fatalf("request was not SigV4 signed: %q", a)
		}
	}
	if err := s3c.Put(ctx, "../x", []byte("x"), "image/png"); err == nil {
		t.Fatalf("invalid key accepted by r2")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// decodeAWSChunked strips the aws-chunked framing minio uses to sign uploads
// over plain HTTP: "<hex size>;chunk-signature=...\r\n<data>\r\n" repeated.
func decodeAWSChunked(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		i := strings.Index(string(b), "\r\n")
		if i < 0 {
			break
		}
		header := string(b[:i])
		size, err := strconv.ParseInt(strings.SplitN(header, ";", 2)[0], 16, 64)
		if err != nil || size == 0 {
			break
		}
		start := i + 2
		out = append(out, b[start:start+int(size)]...)
		b = b[start+int(size)+2:]
	}
	return out
}
