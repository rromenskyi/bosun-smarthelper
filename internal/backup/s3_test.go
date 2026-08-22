package backup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPutObjectSignsAndSendsExpectedRequest(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotContentSHA, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotContentSHA = r.Header.Get("X-Amz-Content-Sha256")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := S3Config{
		Endpoint:        server.URL,
		Region:          "us-east-1",
		Bucket:          "my-bucket",
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secretexample",
	}
	if err := PutObject(context.Background(), cfg, "backups/bosun-backup-2026-01-01.tar.gz", []byte("hello world"), "application/gzip"); err != nil {
		t.Fatalf("PutObject: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Errorf("method = %q, want PUT", gotMethod)
	}
	if gotPath != "/my-bucket/backups/bosun-backup-2026-01-01.tar.gz" {
		t.Errorf("path = %q, want path-style /bucket/key", gotPath)
	}
	if gotBody != "hello world" {
		t.Errorf("body = %q, want hello world", gotBody)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Errorf("Authorization = %q, want an AWS4-HMAC-SHA256 credential for the configured access key", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("Authorization = %q, missing expected SignedHeaders", gotAuth)
	}
	if !strings.Contains(gotAuth, "Signature=") {
		t.Errorf("Authorization = %q, missing a Signature", gotAuth)
	}
	wantSHA := sha256Hex([]byte("hello world"))
	if gotContentSHA != wantSHA {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q", gotContentSHA, wantSHA)
	}
}

func TestPutObjectReturnsErrorOnNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<Error><Code>AccessDenied</Code></Error>"))
	}))
	defer server.Close()

	cfg := S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "b", AccessKeyID: "id", SecretAccessKey: "secret"}
	err := PutObject(context.Background(), cfg, "key", []byte("x"), "application/octet-stream")
	if err == nil {
		t.Fatal("expected an error for a 403 response")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("error = %v, want it to surface the status and body", err)
	}
}
