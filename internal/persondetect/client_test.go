package persondetect

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientDetect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/detect" {
			t.Errorf("method/path = %s %s, want POST /detect", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"person_detected":true,"count":2,"confidence":0.91}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	result, err := client.Detect(context.Background(), []byte("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !result.PersonDetected || result.Count != 2 || result.Confidence != 0.91 {
		t.Errorf("result = %+v, want {true 2 0.91}", result)
	}
}

func TestClientDetectNoPerson(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"person_detected":false,"count":0,"confidence":0}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	result, err := client.Detect(context.Background(), []byte("fake-jpeg-bytes"))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if result.PersonDetected {
		t.Errorf("result = %+v, want person_detected: false", result)
	}
}

func TestClientDetectErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"unsupported image type"}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	if _, err := client.Detect(context.Background(), []byte("not an image")); err == nil {
		t.Error("expected an error for a non-200 response")
	}
}

func TestClientHealthy(t *testing.T) {
	ready := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q, want /health", r.URL.Path)
		}
		if !ready {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, time.Second)
	if client.Healthy(context.Background()) {
		t.Error("expected Healthy() to be false before the server is ready")
	}
	ready = true
	if !client.Healthy(context.Background()) {
		t.Error("expected Healthy() to be true once the server is ready")
	}
}
