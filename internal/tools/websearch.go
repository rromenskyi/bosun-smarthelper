package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

var (
	duckDuckGoResultPattern = regexp.MustCompile(`(?is)<a\s+[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>.*?<a\s+[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	htmlTagPattern          = regexp.MustCompile(`<[^>]+>`)
)

// WebSearchTool searches the public web through DuckDuckGo's HTML endpoint.
type WebSearchTool struct {
	endpoint string
	client   *http.Client
}

// NewWebSearchTool creates a DuckDuckGo-backed search tool.
func NewWebSearchTool(cfg *config.OnlineConfig) *WebSearchTool {
	return &WebSearchTool{endpoint: cfg.DuckDuckGoURL, client: onlineHTTPClient(cfg)}
}

func (t *WebSearchTool) Name() string { return "web_search" }

func (t *WebSearchTool) Description() string {
	return "Search the web with DuckDuckGo for non-news information and return titles, URLs, and short snippets."
}

func (t *WebSearchTool) RequiresNetwork() bool { return true }

func (t *WebSearchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Free-form web search query."},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 15, "description": "Maximum results; defaults to 8."},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func (t *WebSearchTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("search query is required")
	}
	limit, err := integerArgument(args["limit"], 8, 1, 15)
	if err != nil {
		return nil, fmt.Errorf("search limit: %w", err)
	}
	endpoint, err := url.Parse(t.endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse DuckDuckGo URL: %w", err)
	}
	values := endpoint.Query()
	values.Set("q", query)
	values.Set("kl", "wt-wt")
	endpoint.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create DuckDuckGo request: %w", err)
	}
	req.Header.Set("User-Agent", "Bosun/0.1")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DuckDuckGo request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("DuckDuckGo returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read DuckDuckGo response: %w", err)
	}
	matches := duckDuckGoResultPattern.FindAllStringSubmatch(string(body), limit)
	results := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		results = append(results, map[string]any{
			"title":   cleanHTMLText(match[2], 150),
			"url":     unwrapDuckDuckGoURL(html.UnescapeString(match[1])),
			"snippet": cleanHTMLText(match[3], 250),
		})
	}
	return map[string]any{"query": query, "results": results, "count": len(results)}, nil
}

func onlineHTTPClient(cfg *config.OnlineConfig) *http.Client {
	timeout, err := time.ParseDuration(cfg.RequestTimeout)
	if err != nil || timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func integerArgument(value any, defaultValue, minimum, maximum int) (int, error) {
	if value == nil {
		return defaultValue, nil
	}
	var result int
	switch typed := value.(type) {
	case int:
		result = typed
	case float64:
		result = int(typed)
		if float64(result) != typed {
			return 0, fmt.Errorf("must be an integer")
		}
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, fmt.Errorf("must be an integer")
		}
		result = parsed
	default:
		return 0, fmt.Errorf("must be an integer")
	}
	if result < minimum || result > maximum {
		return 0, fmt.Errorf("must be between %d and %d", minimum, maximum)
	}
	return result, nil
}

func cleanHTMLText(value string, limit int) string {
	value = html.UnescapeString(htmlTagPattern.ReplaceAllString(value, ""))
	value = strings.Join(strings.Fields(value), " ")
	if len([]rune(value)) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func unwrapDuckDuckGoURL(value string) string {
	if strings.HasPrefix(value, "//") {
		value = "https:" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.HasSuffix(parsed.Path, "/l/") {
		return value
	}
	if destination := parsed.Query().Get("uddg"); destination != "" {
		return destination
	}
	if destination := parsed.Query().Get("u"); destination != "" {
		return destination
	}
	return value
}
