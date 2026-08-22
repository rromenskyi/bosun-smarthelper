package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/roman220/ai-local-smarthelper/internal/config"
	"github.com/roman220/ai-local-smarthelper/internal/documents"
	"github.com/roman220/ai-local-smarthelper/internal/embeddings"
)

// MemoTool stores and retrieves dated notes in a local JSON file.
type MemoTool struct {
	path         string
	embed        *embeddings.Client
	docs         *documents.Store
	minRelevance float64
	mu           sync.Mutex
}

type memoRecord struct {
	Key        string    `json:"key"`
	Content    string    `json:"content"`
	Status     string    `json:"status"`
	CreatedAt  string    `json:"created_at"`
	UpdatedAt  string    `json:"updated_at"`
	ArchivedAt string    `json:"archived_at,omitempty"`
	Embedding  []float32 `json:"embedding,omitempty"`
	// Tags are free-form, written by the model at the same time as content
	// (no extra LLM call). CanonicalTags are filled in later, out of band,
	// by NormalizeTags mapping Tags onto config.MemoConfig.CanonicalTags —
	// additive, so a free tag is never destroyed by normalization. See
	// docs/memo-search.md.
	Tags             []string `json:"tags,omitempty"`
	CanonicalTags    []string `json:"canonical_tags,omitempty"`
	TagsNormalizedAt string   `json:"tags_normalized_at,omitempty"`
	// Maintenance tracking (all optional; empty MetricName means this
	// memo isn't a maintenance record at all). Deliberately not tied to
	// any one domain — MetricName is a freeform counter name the model
	// chooses per piece of equipment (e.g. "odometer_km" for a car,
	// "main_engine_hours"/"generator_hours" for a boat with more than
	// one engine), not a hardcoded "mileage" field. See docs/memo-search.md.
	MetricName string `json:"metric_name,omitempty"`
	// MetricValue is the counter's value at the time of this record —
	// either a maintenance event ("changed the oil at 55000") or just a
	// standalone reading ("current odometer is 61000"); the latter is how
	// "how much is left" gets answered without any live sensor, by using
	// the most recent record for the same MetricName as "the last known
	// value".
	MetricValue float64 `json:"metric_value,omitempty"`
	// DueDate (RFC3339), when set, is checked against the current date —
	// in Go, not left to the model to reason about — so "maintenance" can
	// reliably report overdue/days-remaining.
	DueDate string `json:"due_date,omitempty"`
	// DueMetricValue, when set, is the counter value this item is next
	// due at; "maintenance" reports it alongside the most recent known
	// MetricValue for the same MetricName, if any.
	DueMetricValue float64 `json:"due_metric_value,omitempty"`
}

type memoFile struct {
	Memos map[string]memoRecord `json:"memos"`
}

// NewMemoTool creates a persistent local memo tool. embedCfg may be nil —
// see config.EmbeddingsConfig for what that degrades to.
func NewMemoTool(cfg *config.MemoConfig, embedCfg *config.EmbeddingsConfig) *MemoTool {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, ".local", "share", "bosun", "memos.json")
		} else {
			path = "memos.json"
		}
	}
	return &MemoTool{path: path, embed: embeddings.NewClient(embedCfg), minRelevance: cfg.MinSearchRelevance}
}

// SetDocumentStore wires in the document store so "search" also ranks
// uploaded-document chunks alongside memos. Optional — nil (the default)
// means search only ever considers memos.
func (t *MemoTool) SetDocumentStore(store *documents.Store) {
	t.docs = store
}

func (t *MemoTool) Name() string {
	return "memo"
}

func (t *MemoTool) Description() string {
	return "Write, read, list, search, topics, maintenance, archive, or delete persistent local memos and uploaded reference documents. Listing exposes timestamps, status, and age_days so old notes can be reviewed. topics lists uploaded documents (title + document_id) with no search needed — check it when unsure whether something is covered, or to find the right document_id to scope a search to it instead of the whole store. Search finds memos and documents by meaning, not just exact words — use it instead of list when the user asks to recall something without naming its exact key, or asks a question a stored document might answer. A search result may include image_url when the source is a diagram — include it in your answer as a markdown image: ![description](image_url). When writing, add a few short lowercase tags describing the topic (e.g. \"purchases\", \"fuel_system\", \"oil\") — use list or search with tag to reliably find every memo on a topic, not just the closest-sounding ones. For equipment upkeep (odometer, engine-hour meters, etc.): write metric_name/metric_value for a reading or event, plus due_date and/or due_metric_value for when the next one is due — compute due_metric_value yourself from a stated interval (\"changed oil at 55000, next in 10000\" -> metric_value:55000, due_metric_value:65000), don't just restate the interval in content. maintenance reports what's due (it computes overdue itself, don't do date math) and lists known_metrics — reuse an existing name for the same equipment. write's response including existing_metric_names means your metric_name matched none of them: re-write immediately with the matching one if this is really the same equipment under a different spelling, but keep your new name if it's genuinely different equipment (a second vehicle, a different engine) — the hint is a check, not proof something's wrong."
}

func (t *MemoTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"write", "read", "list", "search", "topics", "maintenance", "archive", "delete"},
				"description": "Operation to perform. Use list to inspect memo dates and age before archival or deletion; use search to recall a memo/document by meaning; use topics to see what documents exist before deciding whether/where to search; use maintenance to see what equipment upkeep is due.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Short stable memo identifier; required except for list, search, topics, and maintenance.",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Memo text; required for write and omitted for read.",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "For write, a few short lowercase topic tags (e.g. [\"purchases\", \"fuel_system\"]). Omit to leave existing tags on a memo unchanged.",
			},
			"include_archived": map[string]any{
				"type":        "boolean",
				"description": "For list, include archived memos as well as active memos.",
			},
			"tag": map[string]any{
				"type":        "string",
				"description": "For list or search, only consider memos tagged with this exact topic — use for \"show me every memo about X\" instead of relying on similarity alone.",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "For search, the text to find related memos for.",
			},
			"limit": map[string]any{
				"type":        "number",
				"description": "For search, maximum number of results (default 5).",
			},
			"document_id": map[string]any{
				"type":        "string",
				"description": "For search, restrict document results to this one document (see topics for the id) instead of the whole store.",
			},
			"metric_name": map[string]any{
				"type":        "string",
				"description": "For write, a freeform counter name for equipment upkeep, e.g. \"odometer_km\", \"main_engine_hours\", \"generator_hours\" — check maintenance's known_metrics and reuse the exact existing name for the same equipment.",
			},
			"metric_value": map[string]any{
				"type":        "number",
				"description": "For write, the counter's value now — either at a maintenance event, or just a standalone reading (e.g. \"current odometer is 61000\") used as the latest known value for that metric_name.",
			},
			"due_date": map[string]any{
				"type":        "string",
				"description": "For write, a calendar date (YYYY-MM-DD) this item is next due by — maintenance compares it to the real date, not the model.",
			},
			"due_metric_value": map[string]any{
				"type":        "number",
				"description": "For write, the metric_name counter value this item is next due at.",
			},
		},
		"required":             []string{"action"},
		"additionalProperties": false,
	}
}

// hasTag reports whether tag (case-insensitive) is present in either the
// free-form or normalized-canonical tag list.
func hasTag(record memoRecord, tag string) bool {
	tag = strings.ToLower(tag)
	for _, t := range record.Tags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	for _, t := range record.CanonicalTags {
		if strings.ToLower(t) == tag {
			return true
		}
	}
	return false
}

func parseTags(raw any) []string {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	tags := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		s = strings.ToLower(strings.TrimSpace(s))
		if ok && s != "" {
			tags = append(tags, s)
		}
	}
	return tags
}

func (t *MemoTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	action, _ := args["action"].(string)
	key, _ := args["key"].(string)
	action = strings.TrimSpace(action)
	key = strings.TrimSpace(key)
	if action != "list" && action != "search" && action != "topics" && action != "maintenance" && (key == "" || len([]rune(key)) > 128) {
		return nil, fmt.Errorf("memo key must contain 1 to 128 characters")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := t.load()
	if err != nil {
		return nil, err
	}

	switch action {
	case "read":
		record, ok := data.Memos[key]
		if !ok {
			return nil, fmt.Errorf("memo %q was not found", key)
		}
		return memoView(record, time.Now()), nil
	case "list":
		includeArchived, _ := args["include_archived"].(bool)
		tagFilter, _ := args["tag"].(string)
		tagFilter = strings.TrimSpace(tagFilter)
		now := time.Now()
		records := make([]memoRecord, 0, len(data.Memos))
		for _, record := range data.Memos {
			if record.Status == "archived" && !includeArchived {
				continue
			}
			if tagFilter != "" && !hasTag(record, tagFilter) {
				continue
			}
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].UpdatedAt < records[j].UpdatedAt
		})
		views := make([]map[string]any, 0, len(records))
		for _, record := range records {
			views = append(views, memoView(record, now))
		}
		return map[string]any{"memos": views, "count": len(views)}, nil
	case "write":
		content, _ := args["content"].(string)
		content = strings.TrimSpace(content)
		if content == "" {
			return nil, fmt.Errorf("memo content is required for write")
		}
		if len([]rune(content)) > 10000 {
			return nil, fmt.Errorf("memo content must not exceed 10000 characters")
		}
		now := time.Now().Format(time.RFC3339)
		record, exists := data.Memos[key]
		if !exists {
			record = memoRecord{Key: key, CreatedAt: now}
		}
		record.Content = content
		record.Status = "active"
		record.UpdatedAt = now
		record.ArchivedAt = ""
		if rawTags, ok := args["tags"]; ok {
			record.Tags = parseTags(rawTags)
		}
		if rawMetricName, ok := args["metric_name"].(string); ok {
			record.MetricName = strings.TrimSpace(rawMetricName)
		}
		if rawMetricValue, ok := args["metric_value"].(float64); ok {
			record.MetricValue = rawMetricValue
		}
		if rawDueDate, ok := args["due_date"].(string); ok && strings.TrimSpace(rawDueDate) != "" {
			parsed, err := parseFlexibleDate(rawDueDate)
			if err != nil {
				return nil, fmt.Errorf("due_date %q is not a valid date: %w", rawDueDate, err)
			}
			record.DueDate = parsed.Format(time.RFC3339)
		}
		if rawDueMetricValue, ok := args["due_metric_value"].(float64); ok {
			record.DueMetricValue = rawDueMetricValue
		}
		if record.DueMetricValue != 0 && record.MetricName == "" {
			return nil, fmt.Errorf("due_metric_value requires metric_name — there's no counter to compare it against")
		}
		if t.embed != nil {
			// Best-effort: a slow or unreachable embeddings server must
			// never block saving the memo itself. A missing vector just
			// means "search" won't be able to rank this one.
			if vector, err := t.embed.Embed(ctx, content); err == nil {
				record.Embedding = vector
			} else {
				record.Embedding = nil
			}
		}
		data.Memos[key] = record
		if err := t.save(data); err != nil {
			return nil, err
		}
		view := memoView(record, time.Now())
		if record.MetricName != "" {
			// A model deciding on a metric_name only sees known_metrics
			// via the "maintenance" action, which it may not have called
			// this turn — surface the same list here too, so a name that
			// doesn't match any existing one gets flagged immediately,
			// in time for the agent loop's next tool-call round this
			// same turn to fix it, rather than silently fragmenting
			// "odometer_km" into "odometer" and "odometer_km" as two
			// unrelated counters.
			others := make(map[string]bool)
			for otherKey, other := range data.Memos {
				if otherKey != key && other.Status != "archived" && other.MetricName != "" {
					others[other.MetricName] = true
				}
			}
			if len(others) > 0 && !others[record.MetricName] {
				names := make([]string, 0, len(others))
				for name := range others {
					names = append(names, name)
				}
				sort.Strings(names)
				view["existing_metric_names"] = names
			}
		}
		return view, nil
	case "search":
		return t.search(ctx, data, args)
	case "topics":
		return t.topics()
	case "maintenance":
		return t.maintenance(data, time.Now())
	case "archive":
		record, ok := data.Memos[key]
		if !ok {
			return nil, fmt.Errorf("memo %q was not found", key)
		}
		now := time.Now().Format(time.RFC3339)
		record.Status = "archived"
		record.ArchivedAt = now
		record.UpdatedAt = now
		data.Memos[key] = record
		if err := t.save(data); err != nil {
			return nil, err
		}
		return memoView(record, time.Now()), nil
	case "delete":
		record, ok := data.Memos[key]
		if !ok {
			return nil, fmt.Errorf("memo %q was not found", key)
		}
		delete(data.Memos, key)
		if err := t.save(data); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "memo": memoView(record, time.Now())}, nil
	default:
		return nil, fmt.Errorf("unsupported memo action %q", action)
	}
}

type scoredMemo struct {
	record memoRecord
	score  float64
}

// maxSearchResultChars caps how much raw text (memo content or a document
// chunk) rides along with a single search result before it's fed back to
// the LLM. Unbounded text here — up to memo's own 10000-char write limit,
// or documents' 1500-char chunks, times up to `limit` results — risks
// overwhelming a weak model's context and has been observed to trigger
// degenerate output; a search result should point at the answer, not
// paste the whole source.
const maxSearchResultChars = 500

func truncateForSearch(text string) string {
	runes := []rune(text)
	if len(runes) <= maxSearchResultChars {
		return text
	}
	return string(runes[:maxSearchResultChars]) + "…"
}

// search ranks active memos, and uploaded-document chunks when a document
// store is wired in, by meaning against query using cosine similarity over
// stored embeddings. Each side falls back to a plain substring match —
// never an error — when embeddings are disabled, the embeddings server is
// unreachable, or nothing has a vector yet (e.g. written before embeddings
// were configured). See docs/memo-search.md.
func (t *MemoTool) search(ctx context.Context, data memoFile, args map[string]any) (any, error) {
	query, _ := args["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("memo search query is required")
	}
	limit := 5
	if raw, ok := args["limit"].(float64); ok && raw > 0 {
		limit = int(raw)
	}
	tagFilter, _ := args["tag"].(string)
	tagFilter = strings.TrimSpace(tagFilter)

	var candidates []scoredMemo
	if t.embed != nil {
		if queryVector, err := t.embed.Embed(ctx, query); err == nil {
			for _, record := range data.Memos {
				if record.Status == "archived" || len(record.Embedding) == 0 {
					continue
				}
				if tagFilter != "" && !hasTag(record, tagFilter) {
					continue
				}
				candidates = append(candidates, scoredMemo{record, embeddings.CosineSimilarity(queryVector, record.Embedding)})
			}
		}
	}
	if candidates == nil {
		lowerQuery := strings.ToLower(query)
		for _, record := range data.Memos {
			if record.Status == "archived" {
				continue
			}
			if tagFilter != "" && !hasTag(record, tagFilter) {
				continue
			}
			if strings.Contains(strings.ToLower(record.Content), lowerQuery) || strings.Contains(strings.ToLower(record.Key), lowerQuery) {
				candidates = append(candidates, scoredMemo{record, 1})
			}
		}
	}

	results := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		if c.score < t.minRelevance {
			continue
		}
		view := memoView(c.record, time.Now())
		view["source"] = "memo"
		view["relevance"] = c.score
		view["content"] = truncateForSearch(c.record.Content)
		results = append(results, view)
	}

	if t.docs != nil {
		documentID, _ := args["document_id"].(string)
		documentID = strings.TrimSpace(documentID)
		if chunks, err := t.docs.Search(ctx, query, limit, documentID); err == nil {
			for _, chunk := range chunks {
				if chunk.Score < t.minRelevance {
					continue
				}
				result := map[string]any{
					"source":         "document",
					"document_id":    chunk.DocumentID,
					"document_title": chunk.DocumentTitle,
					"text":           truncateForSearch(chunk.Text),
					"relevance":      chunk.Score,
				}
				if chunk.ImageURL != "" {
					// Present as a normal URL the model can drop into a
					// markdown image, same as get_directions' map links —
					// no vision model or OCR needed for the human to see it.
					result["image_url"] = chunk.ImageURL
				}
				results = append(results, result)
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i]["relevance"].(float64) > results[j]["relevance"].(float64)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return map[string]any{"results": results, "count": len(results)}, nil
}

// topics lists uploaded documents (title + id + chunk_count, no chunk
// content) so the model can see what reference material actually exists
// before deciding whether/where to search — cheaper than a search call,
// and lets it recognize a document as the right one to search
// (document_id) even when a query's exact wording wouldn't have scored
// well against that document's chunks directly. Memos aren't listed here;
// use action "list" (optionally with "tag") for those.
func (t *MemoTool) topics() (any, error) {
	if t.docs == nil {
		return map[string]any{"documents": []any{}, "count": 0}, nil
	}
	summaries, err := t.docs.List()
	if err != nil {
		return nil, err
	}
	topics := make([]map[string]any, 0, len(summaries))
	for _, s := range summaries {
		topics = append(topics, map[string]any{
			"document_id": s.ID,
			"title":       s.Title,
			"chunk_count": s.ChunkCount,
		})
	}
	return map[string]any{"documents": topics, "count": len(topics)}, nil
}

// parseFlexibleDate accepts either a bare calendar date (what a model, or a
// user dictating one, is far more likely to say — "2028-08-20") or RFC3339
// (what write itself stores it back as).
func parseFlexibleDate(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	// A bare calendar date (no zone of its own) is parsed as local
	// midnight, not UTC midnight — time.Parse defaults to UTC, which
	// would silently shift "how many days until due" by up to a day
	// depending on the host's offset from UTC when compared against
	// time.Now() (local). RFC3339 already carries an explicit offset, so
	// it's unaffected either way.
	if t, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, raw)
}

// maintenance reports every active memo tracking a due date and/or a due
// counter value (see memoRecord's MetricName/DueDate/DueMetricValue) —
// deliberately not tied to any one domain (a car's odometer, a boat's
// main-engine or generator hour meter, ...). Whether a date-based item is
// overdue is computed here, in Go, against the real clock — not left to
// the model to reason about from a raw date string. A counter-based item
// (no live sensor backs any of this) is paired with the most recent other
// memo sharing the same MetricName, treating it as "the last known
// reading" — e.g. a memo that's just "current odometer 61000" with no due
// fields of its own. known_metrics lists every MetricName seen at all, so
// the model can reuse an existing one instead of inventing a slightly
// different spelling for the same equipment across separate write calls.
func (t *MemoTool) maintenance(data memoFile, now time.Time) (any, error) {
	latestByMetric := make(map[string]struct {
		value float64
		at    time.Time
	})
	knownMetrics := make(map[string]bool)
	for _, record := range data.Memos {
		if record.Status == "archived" || record.MetricName == "" {
			continue
		}
		knownMetrics[record.MetricName] = true
		updatedAt, err := time.Parse(time.RFC3339, record.UpdatedAt)
		if err != nil {
			continue
		}
		if current, ok := latestByMetric[record.MetricName]; !ok || updatedAt.After(current.at) {
			latestByMetric[record.MetricName] = struct {
				value float64
				at    time.Time
			}{record.MetricValue, updatedAt}
		}
	}

	items := make([]map[string]any, 0)
	for _, record := range data.Memos {
		// A due_metric_value with no metric_name is meaningless — no
		// counter to compare it against — and write rejects that
		// combination, but guard here too against a record from before
		// that check existed, or one edited by hand.
		hasDueMetricValue := record.DueMetricValue != 0 && record.MetricName != ""
		if record.Status == "archived" || (record.DueDate == "" && !hasDueMetricValue) {
			continue
		}
		item := map[string]any{"key": record.Key, "content": truncateForSearch(record.Content)}
		if record.MetricName != "" {
			item["metric_name"] = record.MetricName
		}
		if record.DueDate != "" {
			due, err := time.Parse(time.RFC3339, record.DueDate)
			if err == nil {
				item["due_date"] = record.DueDate
				daysUntil := int(due.Sub(now).Hours() / 24)
				item["days_until_due"] = daysUntil
				item["overdue"] = daysUntil < 0
			}
		}
		if record.DueMetricValue != 0 && record.MetricName != "" {
			item["due_metric_value"] = record.DueMetricValue
			if latest, ok := latestByMetric[record.MetricName]; ok {
				item["latest_known_metric_value"] = latest.value
				item["remaining_metric_value"] = record.DueMetricValue - latest.value
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return fmt.Sprint(items[i]["key"]) < fmt.Sprint(items[j]["key"])
	})

	metrics := make([]string, 0, len(knownMetrics))
	for name := range knownMetrics {
		metrics = append(metrics, name)
	}
	sort.Strings(metrics)

	return map[string]any{"items": items, "count": len(items), "known_metrics": metrics}, nil
}

func memoView(record memoRecord, now time.Time) map[string]any {
	status := record.Status
	if status == "" {
		status = "active"
	}
	view := map[string]any{
		"key":        record.Key,
		"content":    record.Content,
		"status":     status,
		"created_at": record.CreatedAt,
		"updated_at": record.UpdatedAt,
		"age_days":   memoAgeDays(record.UpdatedAt, now),
	}
	if record.ArchivedAt != "" {
		view["archived_at"] = record.ArchivedAt
	}
	if len(record.Tags) > 0 {
		view["tags"] = record.Tags
	}
	if len(record.CanonicalTags) > 0 {
		view["canonical_tags"] = record.CanonicalTags
	}
	if record.MetricName != "" {
		view["metric_name"] = record.MetricName
		view["metric_value"] = record.MetricValue
	}
	if record.DueDate != "" {
		view["due_date"] = record.DueDate
	}
	if record.DueMetricValue != 0 {
		view["due_metric_value"] = record.DueMetricValue
	}
	return view
}

func memoAgeDays(updatedAt string, now time.Time) int {
	updated, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil || now.Before(updated) {
		return 0
	}
	return int(now.Sub(updated).Hours() / 24)
}

func (t *MemoTool) load() (memoFile, error) {
	data := memoFile{Memos: make(map[string]memoRecord)}
	file, err := os.Open(t.path)
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("open memo store: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return data, fmt.Errorf("decode memo store: %w", err)
	}
	if data.Memos == nil {
		data.Memos = make(map[string]memoRecord)
	}
	return data, nil
}

func (t *MemoTool) save(data memoFile) error {
	directory := filepath.Dir(t.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create memo directory: %w", err)
	}
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode memo store: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".memos-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary memo store: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set memo store permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write memo store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync memo store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close memo store: %w", err)
	}
	if err := os.Rename(temporaryPath, t.path); err != nil {
		return fmt.Errorf("replace memo store: %w", err)
	}
	return nil
}
