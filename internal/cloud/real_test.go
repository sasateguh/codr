package cloud

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRealBucket talks to a real S3-compatible service when CODR_S3_ENDPOINT is set (MinIO in a throwaway
// container, or a test bucket): put, list, get, delete, and a wrong secret refused. Skipped otherwise.
func TestRealBucket(t *testing.T) {
	ep := os.Getenv("CODR_S3_ENDPOINT")
	if ep == "" {
		t.Skip("CODR_S3_ENDPOINT not set")
	}
	cl := &Client{Endpoint: ep, Region: os.Getenv("CODR_S3_REGION"), Bucket: os.Getenv("CODR_S3_BUCKET"), AccessKey: os.Getenv("CODR_S3_ACCESS"), SecretKey: os.Getenv("CODR_S3_SECRET")}
	ctx := context.Background()
	f := filepath.Join(t.TempDir(), "x")
	payload := bytes.Repeat([]byte("codr real bucket "), 70000) // > 1 MB
	_ = os.WriteFile(f, payload, 0o600)
	key := "ujian/codr-real test+1.tar.gz.enc" // space and plus exercise the canonical URI
	if err := cl.Put(ctx, key, f); err != nil {
		t.Fatal("put:", err)
	}
	objs, err := cl.List(ctx, "ujian/")
	if err != nil || len(objs) != 1 || objs[0].Key != key || objs[0].Size != int64(len(payload)) {
		t.Fatalf("list: %+v %v", objs, err)
	}
	var b bytes.Buffer
	if err := cl.Get(ctx, key, &b); err != nil || !bytes.Equal(b.Bytes(), payload) {
		t.Fatalf("get: %v", err)
	}
	if err := cl.Delete(ctx, key); err != nil {
		t.Fatal("delete:", err)
	}
	if objs, _ := cl.List(ctx, "ujian/"); len(objs) != 0 {
		t.Fatal("not deleted")
	}
	bad := *cl
	bad.SecretKey = "wrong"
	if err := bad.Put(ctx, key, f); err == nil {
		t.Fatal("wrong secret accepted")
	} else {
		t.Log("wrong secret refused:", err)
	}
}
