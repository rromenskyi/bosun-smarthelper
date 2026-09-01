package tools

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/roman220/bosun-smarthelper/internal/config"
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

func TestIntegerArgument(t *testing.T) {
	cases := []struct {
		name    string
		value   any
		want    int
		wantErr bool
	}{
		{"nil uses default", nil, 5, false},
		{"int in range", 3, 3, false},
		{"whole float64", float64(4), 4, false},
		{"fractional float64 rejected", 4.5, 0, true},
		{"numeric string", "7", 7, false},
		{"non-numeric string rejected", "seven", 0, true},
		{"unsupported type rejected", true, 0, true},
		{"below minimum rejected", 0, 0, true},
		{"above maximum rejected", 11, 0, true},
		{"at minimum boundary", 1, 1, false},
		{"at maximum boundary", 10, 10, false},
	}
	for _, c := range cases {
		got, err := integerArgument(c.value, 5, 1, 10)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error, got %d", c.name, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestCleanHTMLText(t *testing.T) {
	cases := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{"strips tags", "<b>Bold</b> and <i>italic</i>", 100, "Bold and italic"},
		{"unescapes entities", "Tom &amp; Jerry", 100, "Tom & Jerry"},
		{"collapses whitespace", "a   b\n\tc", 100, "a b c"},
		{"leaves short text alone", "short", 20, "short"},
		{"truncates with ellipsis", "abcdefghij", 5, "abcd…"},
		{"exact limit is not truncated", "abcde", 5, "abcde"},
	}
	for _, c := range cases {
		if got := cleanHTMLText(c.value, c.limit); got != c.want {
			t.Errorf("%s: cleanHTMLText(%q, %d) = %q, want %q", c.name, c.value, c.limit, got, c.want)
		}
	}
}

func TestUnwrapDuckDuckGoURL(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "unwraps uddg redirect",
			value: "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&rut=1",
			want:  "https://example.com/page",
		},
		{
			name:  "falls back to u parameter",
			value: "https://duckduckgo.com/l/?u=https%3A%2F%2Fexample.com",
			want:  "https://example.com",
		},
		{
			name:  "non-redirect URL passed through unchanged",
			value: "https://example.com/direct",
			want:  "https://example.com/direct",
		},
		{
			name:  "unparseable value passed through unchanged",
			value: "://not a url",
			want:  "://not a url",
		},
		{
			name:  "redirect path with no destination param passed through unchanged",
			value: "https://duckduckgo.com/l/?rut=1",
			want:  "https://duckduckgo.com/l/?rut=1",
		},
	}
	for _, c := range cases {
		if got := unwrapDuckDuckGoURL(c.value); got != c.want {
			t.Errorf("%s: unwrapDuckDuckGoURL(%q) = %q, want %q", c.name, c.value, got, c.want)
		}
	}
}

func TestContainsCyrillic(t *testing.T) {
	if !containsCyrillic("Старпом") {
		t.Error("expected Cyrillic text to be detected")
	}
	if !containsCyrillic("mixed Старпом text") {
		t.Error("expected Cyrillic to be detected even mixed with Latin text")
	}
	if containsCyrillic("Bosun") {
		t.Error("expected plain Latin text to not be detected as Cyrillic")
	}
	if containsCyrillic("") {
		t.Error("expected empty string to not be detected as Cyrillic")
	}
}

func TestWikipediaLanguages(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		preferred string
		want      []string
	}{
		{"Latin title with preferred language", "Boat", "en", []string{"en"}},
		{"Latin title, different preferred language", "Boat", "fr", []string{"fr", "en"}},
		{"Cyrillic title adds ru/uk before the trailing en fallback", "Старпом", "en", []string{"en", "ru", "uk"}},
		{"Cyrillic title with a non-English preferred language", "Старпом", "de", []string{"de", "ru", "uk", "en"}},
		{"empty preferred language is dropped, not left as a blank entry", "Boat", "", []string{"en"}},
		{"duplicates collapsed", "Старпом", "ru", []string{"ru", "uk", "en"}},
	}
	for _, c := range cases {
		got := wikipediaLanguages(c.title, c.preferred)
		if len(got) != len(c.want) {
			t.Errorf("%s: wikipediaLanguages(%q, %q) = %v, want %v", c.name, c.title, c.preferred, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: wikipediaLanguages(%q, %q) = %v, want %v", c.name, c.title, c.preferred, got, c.want)
				break
			}
		}
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
