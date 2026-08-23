package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/roman220/bosun-smarthelper/internal/config"
)

var wikipediaLanguagePattern = regexp.MustCompile(`^[a-z][a-z-]{1,11}$`)

// WikipediaTool retrieves a concise encyclopedia summary.
type WikipediaTool struct {
	endpoint string
	client   *http.Client
}

type wikipediaResponse struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Extract     string `json:"extract"`
	ContentURLs struct {
		Desktop struct {
			Page string `json:"page"`
		} `json:"desktop"`
	} `json:"content_urls"`
}

// NewWikipediaTool creates a Wikipedia REST API tool.
func NewWikipediaTool(cfg *config.OnlineConfig) *WikipediaTool {
	return &WikipediaTool{endpoint: cfg.WikipediaURL, client: onlineHTTPClient(cfg)}
}

func (t *WikipediaTool) Name() string { return "wikipedia" }

func (t *WikipediaTool) Description() string {
	return "Get an encyclopedia summary and article URL from Wikipedia for a person, place, concept, or event."
}

func (t *WikipediaTool) RequiresNetwork() bool { return true }

func (t *WikipediaTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "Article topic or title in the user's language."},
			"lang":  map[string]any{"type": "string", "description": "Preferred Wikipedia language code, such as ru or en."},
		},
		"required":             []string{"title"},
		"additionalProperties": false,
	}
}

func (t *WikipediaTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, fmt.Errorf("wikipedia title is required")
	}
	preferred, _ := args["lang"].(string)
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if preferred != "" && !wikipediaLanguagePattern.MatchString(preferred) {
		return nil, fmt.Errorf("invalid Wikipedia language code %q", preferred)
	}
	languages := wikipediaLanguages(title, preferred)
	for _, language := range languages {
		endpoint := strings.ReplaceAll(t.endpoint, "{lang}", language)
		endpoint = strings.ReplaceAll(endpoint, "{title}", url.PathEscape(strings.ReplaceAll(title, " ", "_")))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("create Wikipedia request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "Bosun/0.1")
		resp, err := t.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("wikipedia request failed: %w", err)
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("wikipedia returned HTTP %d", resp.StatusCode)
		}
		var article wikipediaResponse
		err = json.NewDecoder(resp.Body).Decode(&article)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode Wikipedia response: %w", err)
		}
		return map[string]any{
			"title": article.Title, "description": article.Description,
			"extract": cleanHTMLText(article.Extract, 2000), "url": article.ContentURLs.Desktop.Page,
			"lang": language, "lang_chain_tried": languages,
		}, nil
	}
	return nil, fmt.Errorf("wikipedia article %q was not found in languages %s", title, strings.Join(languages, ", "))
}

func wikipediaLanguages(title, preferred string) []string {
	candidates := []string{preferred}
	if containsCyrillic(title) {
		candidates = append(candidates, "ru", "uk")
	}
	candidates = append(candidates, "en")
	seen := make(map[string]bool)
	result := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			result = append(result, candidate)
		}
	}
	return result
}

func containsCyrillic(value string) bool {
	for _, char := range value {
		if char >= '\u0400' && char <= '\u04ff' {
			return true
		}
	}
	return false
}
