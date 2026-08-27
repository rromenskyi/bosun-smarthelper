package voice

import (
	"context"
	"errors"
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

type fakeSTTEngine struct {
	transcript Transcript
	err        error
	calls      int
}

func (f *fakeSTTEngine) Transcribe(ctx context.Context, wav []byte) (Transcript, error) {
	f.calls++
	return f.transcript, f.err
}

func TestRoutedSTTPrefersRemoteWhileOnline(t *testing.T) {
	remote := &fakeSTTEngine{transcript: Transcript{Text: "from remote"}}
	local := &fakeSTTEngine{transcript: Transcript{Text: "from local"}}
	router := &RoutedSTT{Remote: remote, Local: local, NetworkAvailable: func(context.Context) bool { return true }}

	transcript, err := router.Transcribe(context.Background(), []byte("audio"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if transcript.Text != "from remote" {
		t.Errorf("text = %q, want from remote", transcript.Text)
	}
	if remote.calls != 1 || local.calls != 0 {
		t.Errorf("remote.calls = %d, local.calls = %d, want 1 and 0", remote.calls, local.calls)
	}
}

func TestRoutedSTTFallsBackToLocalWhenOffline(t *testing.T) {
	remote := &fakeSTTEngine{transcript: Transcript{Text: "from remote"}}
	local := &fakeSTTEngine{transcript: Transcript{Text: "from local"}}
	router := &RoutedSTT{Remote: remote, Local: local, NetworkAvailable: func(context.Context) bool { return false }}

	transcript, err := router.Transcribe(context.Background(), []byte("audio"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if transcript.Text != "from local" {
		t.Errorf("text = %q, want from local", transcript.Text)
	}
	if remote.calls != 0 || local.calls != 1 {
		t.Errorf("remote.calls = %d, local.calls = %d, want 0 and 1", remote.calls, local.calls)
	}
}

func TestRoutedSTTFallsBackToLocalWhenRemoteFails(t *testing.T) {
	remote := &fakeSTTEngine{err: errors.New("remote boom")}
	local := &fakeSTTEngine{transcript: Transcript{Text: "from local"}}
	router := &RoutedSTT{Remote: remote, Local: local, NetworkAvailable: func(context.Context) bool { return true }}

	transcript, err := router.Transcribe(context.Background(), []byte("audio"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if transcript.Text != "from local" {
		t.Errorf("text = %q, want from local", transcript.Text)
	}
	if remote.calls != 1 || local.calls != 1 {
		t.Errorf("remote.calls = %d, local.calls = %d, want 1 and 1", remote.calls, local.calls)
	}
}

func TestRoutedSTTReturnsRemoteErrorWhenNoLocalConfigured(t *testing.T) {
	remote := &fakeSTTEngine{err: errors.New("remote boom")}
	router := &RoutedSTT{Remote: remote, NetworkAvailable: func(context.Context) bool { return true }}

	if _, err := router.Transcribe(context.Background(), []byte("audio")); err == nil {
		t.Error("expected the remote error to propagate when no local engine is configured")
	}
}

func TestRoutedSTTErrorsWhenOfflineAndNoLocalConfigured(t *testing.T) {
	remote := &fakeSTTEngine{transcript: Transcript{Text: "from remote"}}
	router := &RoutedSTT{Remote: remote, NetworkAvailable: func(context.Context) bool { return false }}

	if _, err := router.Transcribe(context.Background(), []byte("audio")); err == nil {
		t.Error("expected an error when offline with no local engine configured")
	}
	if remote.calls != 0 {
		t.Errorf("remote.calls = %d, want 0 (never worth trying while offline)", remote.calls)
	}
}
