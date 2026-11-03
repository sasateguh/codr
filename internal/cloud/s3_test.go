package cloud

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSignAWSExample reproduces the GET Object example of the AWS Signature V4 documentation
// ("Signature Calculations for the Authorization Header", example: GET Object with a Range header).
func TestSignAWSExample(t *testing.T) {
	c := &Client{Region: "us-east-1", AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
	req, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	c.sign(req, emptySHA)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("authorization\n got %s\nwant %s", got, want)
	}
}

func TestEncodePath(t *testing.T) {
	if got := encodePath("/bucket/codr/a b+c.enc"); got != "/bucket/codr/a%20b%2Bc.enc" {
		t.Fatal(got)
	}
	if !strings.Contains(canonicalQuery(map[string][]string{"prefix": {"codr/"}, "list-type": {"2"}}), "list-type=2&prefix=codr%2F") {
		t.Fatal("query not canonical")
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	at := func(h int) string { return now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339Nano) }
	for _, c := range []struct {
		last, status string
		want         bool
	}{
		{"", "", true}, {at(1), "ok", false}, {at(25), "ok", true}, {at(2), "failed", true},
		{at(0), "failed", false}, {at(30), "running", false},
	} {
		if got := Due(c.last, c.status, 24, now); got != c.want {
			t.Errorf("Due(%s, %s) = %v", c.last, c.status, got)
		}
	}
}
