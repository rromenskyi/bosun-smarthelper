// Package backup builds a manual, on-demand snapshot of Bosun's persistent
// data and uploads it to any S3-compatible object store (AWS S3, Backblaze
// B2, MinIO, Wasabi, ...) — invoked by hand (smarthelper backup), never on
// a schedule, so it never costs bandwidth without the user asking for it.
package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Config is the minimal connection info needed to talk to any
// S3-compatible endpoint. Path-style URLs only (https://endpoint/bucket/key)
// — not every S3-compatible provider (self-hosted MinIO in particular)
// supports virtual-hosted-style (bucket-as-subdomain) URLs, but every one
// of them supports path-style.
type S3Config struct {
	Endpoint        string // e.g. "https://s3.us-west-002.backblazeb2.com"
	Region          string // some providers require a real AWS-style region; others accept any non-empty string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// PutObject uploads body to key via a single SigV4-signed PUT request.
//
// No AWS SDK: this project needs exactly a handful of S3 operations
// (PutObject, GetObject, ListObjects — no multipart, no streaming chunked
// signing), and hand-rolling those avoids a dependency tree several times
// the size of the project itself. See signRequest for the shared signing
// algorithm, verified live against a real MinIO server (see s3_test.go)
// rather than hand-checked against a remembered reference value.
func PutObject(ctx context.Context, cfg S3Config, key string, body []byte, contentType string) error {
	canonicalURI := objectURI(cfg.Bucket, key)
	extraHeaders := map[string]string{"content-type": contentType}
	resp, err := signedRequest(ctx, cfg, http.MethodPut, canonicalURI, "", body, extraHeaders)
	if err != nil {
		return fmt.Errorf("upload to S3: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("S3 upload failed: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// GetObject downloads key's full content.
func GetObject(ctx context.Context, cfg S3Config, key string) ([]byte, error) {
	resp, err := signedRequest(ctx, cfg, http.MethodGet, objectURI(cfg.Bucket, key), "", nil, nil)
	if err != nil {
		return nil, fmt.Errorf("download from S3: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read S3 response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("S3 download failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

// ObjectInfo describes one object as returned by ListObjects.
type ObjectInfo struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// listBucketResult is the standard S3 ListObjectsV2 XML response shape —
// every S3-compatible provider returns this same schema.
type listBucketResult struct {
	Contents []struct {
		Key          string    `xml:"Key"`
		Size         int64     `xml:"Size"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
}

// ListObjects lists every object in the bucket under prefix (pass "" for
// everything), using ListObjectsV2 (list-type=2) — the version every
// current S3-compatible provider supports; the original ListObjects (v1)
// is legacy even on AWS itself.
func ListObjects(ctx context.Context, cfg S3Config, prefix string) ([]ObjectInfo, error) {
	query := url.Values{"list-type": {"2"}}
	if prefix != "" {
		query.Set("prefix", prefix)
	}
	canonicalURI := "/" + cfg.Bucket + "/"
	resp, err := signedRequest(ctx, cfg, http.MethodGet, canonicalURI, canonicalQueryString(query), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("list S3 objects: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read S3 response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("S3 list failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var result listBucketResult
	if err := xml.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse S3 list response: %w", err)
	}
	objects := make([]ObjectInfo, 0, len(result.Contents))
	for _, c := range result.Contents {
		objects = append(objects, ObjectInfo{Key: c.Key, Size: c.Size, LastModified: c.LastModified})
	}
	return objects, nil
}

func objectURI(bucket, key string) string {
	return "/" + bucket + "/" + strings.TrimPrefix(key, "/")
}

// canonicalQueryString renders query in AWS's canonical form: parameters
// sorted by key (url.Values.Encode already guarantees this) and values
// percent-encoded per RFC 3986, where a space is "%20" — url.Values.Encode
// uses form-encoding instead (a space as "+"), so that one substitution is
// applied on top of it. None of this package's own query values ever
// contain a space in practice, but getting it right costs nothing and
// avoids a latent signature mismatch for anyone who does pass one.
func canonicalQueryString(query url.Values) string {
	return strings.ReplaceAll(query.Encode(), "+", "%20")
}

const amzDateFormat = "20060102T150405Z"

// signedRequest builds, signs, and sends one request. extraHeaders (may be
// nil) are additional headers to both set on the request and include in
// the signature — every request always signs host, x-amz-content-sha256,
// and x-amz-date on top of whatever's passed here.
func signedRequest(ctx context.Context, cfg S3Config, method, canonicalURI, canonicalQuery string, body []byte, extraHeaders map[string]string) (*http.Response, error) {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse S3 endpoint: %w", err)
	}
	reqURL := *endpoint
	reqURL.Path = canonicalURI
	reqURL.RawQuery = canonicalQuery

	now := time.Now().UTC()
	payloadHash := sha256Hex(body)

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	req.Header.Set("Host", endpoint.Host)
	req.Header.Set("X-Amz-Date", now.Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	for name, value := range extraHeaders {
		req.Header.Set(name, value)
	}

	signedHeaderNames := headerNamesToSign(extraHeaders)
	req.Header.Set("Authorization", signV4(cfg, method, canonicalURI, canonicalQuery, req.Header, signedHeaderNames, payloadHash, now))

	return http.DefaultClient.Do(req)
}

// headerNamesToSign is host + x-amz-content-sha256 + x-amz-date (present
// on every request) plus whatever extraHeaders this particular request
// also sets (e.g. content-type on a PUT) — sorted, since AWS requires
// SignedHeaders and the canonical header block in strict alphabetical
// order.
func headerNamesToSign(extraHeaders map[string]string) []string {
	names := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	for name := range extraHeaders {
		names = append(names, strings.ToLower(name))
	}
	sort.Strings(names)
	return names
}

// signV4 computes an AWS Signature Version 4 Authorization header value
// for a single request with an already-known, fully-buffered payload hash
// — no chunked/streaming signing, which this package's plain object
// PUT/GET/LIST never need. See
// https://docs.aws.amazon.com/general/latest/gr/sigv4-create-canonical-request.html
// for the algorithm this implements step for step.
func signV4(cfg S3Config, method, canonicalURI, canonicalQuery string, headers http.Header, signedHeaderNames []string, payloadHash string, now time.Time) string {
	dateStamp := now.Format("20060102")

	var canonicalHeaders strings.Builder
	for _, h := range signedHeaderNames {
		fmt.Fprintf(&canonicalHeaders, "%s:%s\n", h, strings.TrimSpace(headers.Get(h)))
	}
	signedHeadersStr := strings.Join(signedHeaderNames, ";")

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeadersStr,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, cfg.Region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		now.Format(amzDateFormat),
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	dateKey := hmacSHA256([]byte("AWS4"+cfg.SecretAccessKey), dateStamp)
	regionKey := hmacSHA256(dateKey, cfg.Region)
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	return fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		cfg.AccessKeyID, credentialScope, signedHeadersStr, signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
