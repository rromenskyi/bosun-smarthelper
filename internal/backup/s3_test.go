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

func TestGetObjectSignsAndReturnsBody(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("archive contents"))
	}))
	defer server.Close()

	cfg := S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "my-bucket", AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secretexample"}
	body, err := GetObject(context.Background(), cfg, "backups/x.tar.gz")
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if string(body) != "archive contents" {
		t.Errorf("body = %q, want archive contents", body)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}
	if gotPath != "/my-bucket/backups/x.tar.gz" {
		t.Errorf("path = %q, want path-style /bucket/key", gotPath)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("Authorization = %q, a GET must not sign content-type (never set on this request)", gotAuth)
	}
}

func TestListObjectsParsesResultsAndSignsQueryString(t *testing.T) {
	var gotQuery, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/xml")
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult>
  <Contents><Key>bosun-backup-2026-01-01T00-00-00Z.tar.gz</Key><Size>1234</Size><LastModified>2026-01-01T00:00:00.000Z</LastModified></Contents>
  <Contents><Key>bosun-backup-2026-01-02T00-00-00Z.tar.gz</Key><Size>5678</Size><LastModified>2026-01-02T00:00:00.000Z</LastModified></Contents>
</ListBucketResult>`))
	}))
	defer server.Close()

	cfg := S3Config{Endpoint: server.URL, Region: "us-east-1", Bucket: "my-bucket", AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secretexample"}
	objects, err := ListObjects(context.Background(), cfg, "bosun-backup-")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if !strings.Contains(gotQuery, "list-type=2") || !strings.Contains(gotQuery, "prefix=bosun-backup-") {
		t.Errorf("query = %q, want list-type=2 and the prefix", gotQuery)
	}
	if !strings.Contains(gotAuth, "AWS4-HMAC-SHA256") {
		t.Errorf("Authorization = %q, want a signed request even for a list", gotAuth)
	}
	if len(objects) != 2 {
		t.Fatalf("objects = %+v, want 2", objects)
	}
	if objects[0].Key != "bosun-backup-2026-01-01T00-00-00Z.tar.gz" || objects[0].Size != 1234 {
		t.Errorf("objects[0] = %+v", objects[0])
	}
	if objects[1].Key != "bosun-backup-2026-01-02T00-00-00Z.tar.gz" || objects[1].Size != 5678 {
		t.Errorf("objects[1] = %+v", objects[1])
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
