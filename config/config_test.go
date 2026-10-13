package config

import "testing"

func setBase(t *testing.T) {
	t.Setenv("TVT_JWT_SECRET", "0123456789abcdef0123456789abcdef0123")
}

func TestS3RequiresPublicURL(t *testing.T) {
	setBase(t)
	t.Setenv("TVT_S3_ENDPOINT", "https://acct.r2.cloudflarestorage.com")
	t.Setenv("TVT_S3_BUCKET", "b")
	t.Setenv("TVT_S3_ACCESS_KEY_ID", "k")
	t.Setenv("TVT_S3_SECRET_ACCESS_KEY", "s")
	if _, err := Load(); err == nil {
		t.Fatalf("a bucket without TVT_MEDIA_PUBLIC_URL must be refused")
	}
	t.Setenv("TVT_MEDIA_PUBLIC_URL", "https://cdn.example.net")
	if _, err := Load(); err != nil {
		t.Fatalf("complete bucket config refused: %v", err)
	}
}

func TestNoBucketMeansLocalDisk(t *testing.T) {
	setBase(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.S3.Configured() {
		t.Fatalf("no S3 settings must mean local disk")
	}
}
