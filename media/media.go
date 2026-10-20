// Package media stores binary objects (profile images and the cached TMDB
// artwork) on local disk or, when configured, in any S3-compatible
// bucket (Cloudflare R2, AWS S3, MinIO, Backblaze B2, ...).
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var ErrNotFound = errors.New("media: object not found")

// Object is an opened stored object; the caller must Close Body.
type Object struct {
	Body        io.ReadCloser
	Size        int64
	ContentType string
}

type Store interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	Get(ctx context.Context, key string) (*Object, error)
	Delete(ctx context.Context, key string) error
	Kind() string
	// PublicURL is where a browser can fetch the object directly, or "" when the
	// store is private and the app must serve it.
	PublicURL(key string) string
}

var keyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_./-]{0,200}$`)

// ValidKey rejects anything that could escape the store's root.
func ValidKey(key string) bool {
	if !keyRe.MatchString(key) {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func contentTypeFor(key string) string {
	switch strings.ToLower(filepath.Ext(key)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

type Disk struct{ Dir string }

func (d *Disk) Kind() string { return "disk" }

func (d *Disk) PublicURL(string) string { return "" }

func (d *Disk) path(key string) (string, error) {
	if !ValidKey(key) {
		return "", fmt.Errorf("media: invalid key %q", key)
	}
	return filepath.Join(d.Dir, filepath.FromSlash(key)), nil
}

func (d *Disk) Put(ctx context.Context, key string, data []byte, contentType string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("media: create dir for %s: %w", key, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".upload-*")
	if err != nil {
		return fmt.Errorf("media: temp file for %s: %w", key, err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("media: write %s: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("media: close %s: %w", key, err)
	}
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return fmt.Errorf("media: publish %s: %w", key, err)
	}
	return nil
}

func (d *Disk) Get(ctx context.Context, key string) (*Object, error) {
	p, err := d.path(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("media: open %s: %w", key, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("media: stat %s: %w", key, err)
	}
	return &Object{Body: f, Size: st.Size(), ContentType: contentTypeFor(key)}, nil
}

func (d *Disk) Delete(ctx context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media: delete %s: %w", key, err)
	}
	return nil
}

type S3Config struct {
	Endpoint, Region, Bucket, AccessKeyID, SecretAccessKey, Prefix string
	// PublicURL, when set, is a public base (for example a bucket custom
	// domain) from which objects are served directly to browsers.
	PublicURL string
}

// Configured is true when every required S3 setting is present; Partial is true
// when some are set but not all, which is a misconfiguration to refuse at boot.
func (c S3Config) Configured() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKeyID != "" && c.SecretAccessKey != ""
}

func (c S3Config) Partial() bool {
	any := c.Endpoint != "" || c.Bucket != "" || c.AccessKeyID != "" || c.SecretAccessKey != ""
	return any && !c.Configured()
}

type S3 struct {
	client *minio.Client
	bucket string
	prefix string
	public string
}

func NewS3(c S3Config) (*S3, error) {
	if !c.Configured() {
		return nil, errors.New("media: S3 storage needs an endpoint, bucket, access key id and secret")
	}
	endpoint := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(c.Endpoint), "https://"), "http://")
	region := strings.TrimSpace(c.Region)
	if region == "" {
		if strings.Contains(endpoint, "r2.cloudflarestorage.com") {
			region = "auto"
		} else {
			region = "us-east-1"
		}
	}
	secure := !strings.HasPrefix(strings.TrimSpace(c.Endpoint), "http://")
	cl, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(c.AccessKeyID, c.SecretAccessKey, ""), Secure: secure, Region: region})
	if err != nil {
		return nil, fmt.Errorf("media: s3 client: %w", err)
	}
	prefix := strings.Trim(strings.TrimSpace(c.Prefix), "/")
	if prefix != "" {
		prefix += "/"
	}
	return &S3{client: cl, bucket: c.Bucket, prefix: prefix, public: strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")}, nil
}

func (r *S3) Kind() string { return "s3" }

func (r *S3) PublicURL(key string) string {
	if r.public == "" || !ValidKey(key) {
		return ""
	}
	return r.public + "/" + r.prefix + key
}

// cacheControl: artwork is content-addressed and immutable; user images can be
// removed, so they are cached for a day only.
func cacheControl(key string) string {
	if strings.HasPrefix(key, "tmdb/") {
		return "public, max-age=31536000, immutable"
	}
	return "public, max-age=86400"
}

func (r *S3) objectKey(key string) (string, error) {
	if !ValidKey(key) {
		return "", fmt.Errorf("media: invalid key %q", key)
	}
	return r.prefix + key, nil
}

func (r *S3) Put(ctx context.Context, key string, data []byte, contentType string) error {
	k, err := r.objectKey(key)
	if err != nil {
		return err
	}
	if _, err := r.client.PutObject(ctx, r.bucket, k, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: contentType, CacheControl: cacheControl(key)}); err != nil {
		return fmt.Errorf("media: s3 put %s: %w", key, err)
	}
	return nil
}

func (r *S3) Get(ctx context.Context, key string) (*Object, error) {
	k, err := r.objectKey(key)
	if err != nil {
		return nil, err
	}
	obj, err := r.client.GetObject(ctx, r.bucket, k, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("media: s3 get %s: %w", key, err)
	}
	st, err := obj.Stat()
	if err != nil {
		obj.Close()
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("media: s3 stat %s: %w", key, err)
	}
	ct := st.ContentType
	if ct == "" || ct == "application/octet-stream" {
		ct = contentTypeFor(key)
	}
	return &Object{Body: obj, Size: st.Size, ContentType: ct}, nil
}

func (r *S3) Delete(ctx context.Context, key string) error {
	k, err := r.objectKey(key)
	if err != nil {
		return err
	}
	if err := r.client.RemoveObject(ctx, r.bucket, k, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("media: s3 delete %s: %w", key, err)
	}
	return nil
}
