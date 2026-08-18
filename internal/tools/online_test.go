package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestWebSearchTool(t *testing.T) {
	tool := NewWebSearchTool(&config.OnlineConfig{DuckDuckGoURL: "https://search.example/html", RequestTimeout: "1s"})
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("q") != "bosun" {
			t.Errorf("query = %q, want bosun", req.URL.Query().Get("q"))
		}
		body := `<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com">Example <b>title</b></a>` +
			`<a class="result__snippet">Useful &amp; short snippet</a>`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{"query": "bosun", "limit": float64(1)})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	results := result.(map[string]any)["results"].([]map[string]any)
	if len(results) != 1 || results[0]["url"] != "https://example.com" || results[0]["title"] != "Example title" {
		t.Errorf("unexpected search results: %#v", results)
	}
}

func TestWikipediaTool(t *testing.T) {
	tool := NewWikipediaTool(&config.OnlineConfig{
		WikipediaURL:   "https://{lang}.wikipedia.example/page/{title}",
		RequestTimeout: "1s",
	})
	tool.client.Transport = weatherRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "ru.wikipedia.example" {
			t.Errorf("Wikipedia host = %q, want ru.wikipedia.example", req.URL.Host)
		}
		body := `{"title":"Старпом","description":"должность","extract":"Помощник капитана.","content_urls":{"desktop":{"page":"https://ru.wikipedia.org/wiki/test"}}}`
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})

	result, err := tool.Execute(context.Background(), map[string]any{"title": "Старпом", "lang": "ru"})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	article := result.(map[string]any)
	if article["extract"] != "Помощник капитана." || article["lang"] != "ru" {
		t.Errorf("unexpected Wikipedia result: %#v", article)
	}
}
