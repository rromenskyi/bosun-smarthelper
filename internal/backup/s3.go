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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// S3Config is the minimal connection info needed to PUT an object to any
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
// No AWS SDK: this project needs exactly this one S3 operation, and
// hand-rolling that avoids a dependency tree several times the size of the
// project itself for a request whose signing algorithm — for an
// already-fully-buffered body, no multipart, no streaming chunked signing
// — is short, self-contained, and well documented. See signV4 for the
// algorithm itself, verified live against a real MinIO server (see
// s3_test.go) rather than hand-checked against a remembered reference
// value.
func PutObject(ctx context.Context, cfg S3Config, key string, body []byte, contentType string) error {
	endpoint, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return fmt.Errorf("parse S3 endpoint: %w", err)
	}
	canonicalURI := "/" + cfg.Bucket + "/" + strings.TrimPrefix(key, "/")
	reqURL := *endpoint
	reqURL.Path = canonicalURI

	now := time.Now().UTC()
	payloadHash := sha256Hex(body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Host", endpoint.Host)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Amz-Date", now.Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	req.Header.Set("Authorization", signV4(cfg, http.MethodPut, canonicalURI, req.Header, payloadHash, now))

	resp, err := http.DefaultClient.Do(req)
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

const amzDateFormat = "20060102T150405Z"

// signedHeaderNames must stay sorted (AWS requires SignedHeaders and the
// canonical header block in strict alphabetical order) and must exactly
// match the headers PutObject actually sets on the request before calling
// this.
var signedHeaderNames = []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}

// signV4 computes an AWS Signature Version 4 Authorization header value
// for a single request with an already-known, fully-buffered payload hash
// — no chunked/streaming signing, which real S3-compatible object PUTs
// don't need. See https://docs.aws.amazon.com/general/latest/gr/sigv4-create-canonical-request.html
// for the algorithm this implements step for step.
func signV4(cfg S3Config, method, canonicalURI string, headers http.Header, payloadHash string, now time.Time) string {
	dateStamp := now.Format("20060102")

	var canonicalHeaders strings.Builder
	for _, h := range signedHeaderNames {
		fmt.Fprintf(&canonicalHeaders, "%s:%s\n", h, strings.TrimSpace(headers.Get(h)))
	}
	signedHeadersStr := strings.Join(signedHeaderNames, ";")

	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		"", // no query string on a plain object PUT
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
