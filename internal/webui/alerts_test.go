package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func postAlertsTest(server *Server, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/api/alerts/test", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestHandleAlertsTestUnconfiguredChannelReturnsNotImplemented(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAlertsConfigured(false, false, false)
	server.SetAlertsTestSender(func(context.Context, string) error {
		t.Fatal("sender must not be called for an unconfigured channel")
		return nil
	})

	response := postAlertsTest(server, `{"channel":"speaker"}`)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
}

func TestHandleAlertsTestUnknownChannelReturnsNotImplemented(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAlertsConfigured(true, true, true)

	response := postAlertsTest(server, `{"channel":"carrier-pigeon"}`)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
}

func TestHandleAlertsTestWithoutSenderWiredReturnsNotImplemented(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAlertsConfigured(true, true, true)
	// SetAlertsTestSender deliberately not called.

	response := postAlertsTest(server, `{"channel":"telegram"}`)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotImplemented)
	}
}

func TestHandleAlertsTestSendsThroughTheRequestedChannel(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAlertsConfigured(true, true, true)
	var gotChannel string
	server.SetAlertsTestSender(func(_ context.Context, channel string) error {
		gotChannel = channel
		return nil
	})

	response := postAlertsTest(server, `{"channel":"webhook"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if gotChannel != "webhook" {
		t.Errorf("sender called with channel = %q, want webhook", gotChannel)
	}
	var body map[string]bool
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body["sent"] {
		t.Errorf("body = %#v, want sent: true", body)
	}
}

func TestHandleAlertsTestSurfacesSenderErrorAsBadGateway(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAlertsConfigured(true, true, true)
	server.SetAlertsTestSender(func(context.Context, string) error {
		return errors.New("dial tcp: connection refused")
	})

	response := postAlertsTest(server, `{"channel":"telegram"}`)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadGateway)
	}
	if !strings.Contains(response.Body.String(), "connection refused") {
		t.Errorf("body = %s, want the real error surfaced for debugging", response.Body.String())
	}
}

func TestHandleAlertsTestRejectsMalformedBody(t *testing.T) {
	server := NewServer(&fakeAsker{}, nil, time.Second, "ru", nil)
	server.SetAlertsConfigured(true, true, true)

	response := postAlertsTest(server, `not json`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}
