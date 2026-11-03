// Package cloud keeps an encrypted copy of a backup in an S3-compatible bucket the owner controls (D70):
// AWS S3, Cloudflare R2, Backblaze B2, Wasabi, MinIO. The client is a small Signature V4 implementation on
// the standard library (path-style URLs, one PUT per object), so no SDK is pulled in.
package cloud

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client talks to one bucket.
type Client struct {
	Endpoint  string // https://<account>.r2.cloudflarestorage.com, https://s3.us-west-004.backblazeb2.com, …
	Region    string // "auto" for R2; the bucket's region elsewhere
	Bucket    string
	AccessKey string
	SecretKey string
	HTTP      *http.Client
	now       func() time.Time
}

// Object is one listed key.
type Object struct {
	Key      string
	Size     int64
	Modified time.Time
}

const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// maxPut is the largest single PUT S3 accepts.
const maxPut = 5 << 30

// ErrStatus is a non-2xx answer; Body holds the start of the error document.
type ErrStatus struct {
	Code int
	Body string
}

func (e ErrStatus) Error() string { return "s3: HTTP " + strconv.Itoa(e.Code) + ": " + e.Body }

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

func (c *Client) objectURL(key string, q url.Values) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(c.Endpoint, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("s3: endpoint must be an http(s) URL")
	}
	u.Path = "/" + c.Bucket
	if key != "" {
		u.Path += "/" + key
	}
	u.RawQuery = q.Encode()
	return u, nil
}

// Put uploads the file at path as key.
func (c *Client) Put(ctx context.Context, key, path string) error {
	f, err := os.Open(path) // #nosec G304 -- our own temp file
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > maxPut {
		return fmt.Errorf("s3: %d bytes is above the single-PUT limit", st.Size())
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	u, err := c.objectURL(key, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	return c.do(req, hex.EncodeToString(h.Sum(nil)), nil)
}

// Get streams key into w.
func (c *Client) Get(ctx context.Context, key string, w io.Writer) error {
	u, err := c.objectURL(key, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	return c.do(req, emptySHA, w)
}

// Delete removes key.
func (c *Client) Delete(ctx context.Context, key string) error {
	u, err := c.objectURL(key, nil)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return err
	}
	return c.do(req, emptySHA, nil)
}

// List returns every key under prefix (ListObjectsV2, all pages), oldest first by key.
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for page := 0; page < 1000; page++ {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u, err := c.objectURL("", q)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		if err := c.do(req, emptySHA, &b); err != nil {
			return nil, err
		}
		var doc struct {
			Contents []struct {
				Key          string
				Size         int64
				LastModified time.Time
			}
			IsTruncated           bool
			NextContinuationToken string
		}
		if err := xml.Unmarshal([]byte(b.String()), &doc); err != nil {
			return nil, fmt.Errorf("s3: list: %w", err)
		}
		for _, o := range doc.Contents {
			out = append(out, Object{Key: o.Key, Size: o.Size, Modified: o.LastModified})
		}
		if !doc.IsTruncated || doc.NextContinuationToken == "" {
			break
		}
		token = doc.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (c *Client) do(req *http.Request, payloadSHA string, w io.Writer) error {
	c.sign(req, payloadSHA)
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ErrStatus{Code: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if w != nil {
		_, err = io.Copy(w, resp.Body)
	}
	return err
}

// sign adds a Signature Version 4 Authorization header for service s3. Signed headers: host,
// x-amz-content-sha256, x-amz-date, and range when present.
func (c *Client) sign(req *http.Request, payloadSHA string) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	t := now().UTC()
	amzDate, day := t.Format("20060102T150405Z"), t.Format("20060102")
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadSHA)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	names := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	vals := map[string]string{"host": host, "x-amz-content-sha256": payloadSHA, "x-amz-date": amzDate}
	if r := req.Header.Get("Range"); r != "" {
		names = append(names, "range")
		vals["range"] = r
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + strings.TrimSpace(vals[n]) + "\n")
	}
	signed := strings.Join(names, ";")
	canon := strings.Join([]string{req.Method, encodePath(req.URL.Path), canonicalQuery(req.URL.Query()), ch.String(), signed, payloadSHA}, "\n")
	scope := day + "/" + c.Region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA([]byte("AWS4"+c.SecretKey), day)
	k = hmacSHA(k, c.Region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hmacSHA(key []byte, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return m.Sum(nil)
}

// encodePath URI-encodes each segment (RFC 3986 unreserved kept), slashes kept: S3's canonical URI.
func encodePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k)+"="+uriEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func uriEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
