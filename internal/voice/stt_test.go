package voice

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhisperCppSTTTranscribe(t *testing.T) {
	var gotLanguage, gotFormat, gotFilename string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			t.Errorf("path = %q, want /inference", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		gotLanguage = r.FormValue("language")
		gotFormat = r.FormValue("response_format")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read uploaded file: %v", err)
		}
		defer file.Close()
		gotFilename = header.Filename

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":" Капитан! Старпом на связи.\n"}`))
	}))
	defer server.Close()

	engine := &WhisperCppSTT{BaseURL: server.URL, Language: "ru"}
	transcript, err := engine.Transcribe(context.Background(), []byte("fake wav bytes"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if transcript.Text != " Капитан! Старпом на связи.\n" {
		t.Errorf("text = %q", transcript.Text)
	}
	if transcript.Language != "ru" {
		t.Errorf("language = %q, want ru", transcript.Language)
	}
	if gotLanguage != "ru" {
		t.Errorf("server received language = %q, want ru", gotLanguage)
	}
	if gotFormat != "json" {
		t.Errorf("server received response_format = %q, want json", gotFormat)
	}
	if gotFilename == "" {
		t.Error("server did not receive an uploaded file")
	}
}

func TestWhisperCppSTTTranscribePropagatesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer server.Close()

	engine := &WhisperCppSTT{BaseURL: server.URL}
	if _, err := engine.Transcribe(context.Background(), []byte("audio")); err == nil {
		t.Error("expected an error for a non-200 response")
	}
}

func TestRemoteSTTTranscribe(t *testing.T) {
	var gotPath, gotModel, gotLanguage, gotFormat, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		gotModel = r.FormValue("model")
		gotLanguage = r.FormValue("language")
		gotFormat = r.FormValue("response_format")
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("read uploaded file: %v", err)
		}
		defer file.Close()

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"Капитан! Старпом на связи."}`))
	}))
	defer server.Close()

	engine := &RemoteSTT{BaseURL: server.URL, Model: "whisper-large-v3-turbo", APIKey: "test-key", Language: "ru"}
	transcript, err := engine.Transcribe(context.Background(), []byte("fake wav bytes"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if transcript.Text != "Капитан! Старпом на связи." {
		t.Errorf("text = %q", transcript.Text)
	}
	if gotPath != "/audio/transcriptions" {
		t.Errorf("path = %q, want /audio/transcriptions", gotPath)
	}
	if gotModel != "whisper-large-v3-turbo" {
		t.Errorf("server received model = %q", gotModel)
	}
	if gotLanguage != "ru" {
		t.Errorf("server received language = %q, want ru", gotLanguage)
	}
	if gotFormat != "json" {
		t.Errorf("server received response_format = %q, want json", gotFormat)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization header = %q, want Bearer test-key", gotAuth)
	}
}

func TestRemoteSTTTranscribePropagatesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid api key"))
	}))
	defer server.Close()

	engine := &RemoteSTT{BaseURL: server.URL, APIKey: "bad-key"}
	if _, err := engine.Transcribe(context.Background(), []byte("audio")); err == nil {
		t.Error("expected an error for a non-200 response")
	}
}
