package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"encoding/json"
	"path/filepath"

	"github.com/roman220/ai-local-smarthelper/internal/llm"
)

// MetricMergeSuggestion is a pending (or already-decided) proposal from
// CheckMetricMerges that two metric_name spellings are the same physical
// counter — e.g. "odometer_miles" and "oil_change_odometer" both tracking
// a car's mileage. It's surfaced to a human via the web UI's approval
// queue (docs/maintenance-tracking.md); DecideMetricMerge is the only way
// one of these actually changes anything on disk — the model's own
// judgment never merges data by itself.
type MetricMergeSuggestion struct {
	ID         string   `json:"id"`
	Names      []string `json:"names"`
	Canonical  string   `json:"canonical"`
	Status     string   `json:"status"` // "pending", "approved", "rejected"
	ProposedAt string   `json:"proposed_at"`
	DecidedAt  string   `json:"decided_at,omitempty"`
}

type metricMergeFile struct {
	Suggestions []MetricMergeSuggestion `json:"suggestions"`
}

// metricMergeID is a stable, order-independent identifier for a pair of
// metric names — also how CheckMetricMerges recognizes a pair it has
// already proposed (pending or decided either way) and skips it, so a
// rejected pair is never re-proposed and an approved one can't recur
// (its names are gone from known_metrics once merged).
func metricMergeID(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func (t *MemoTool) metricMergePath() string {
	return filepath.Join(filepath.Dir(t.path), "metric_merges.json")
}

func (t *MemoTool) loadMetricMerges() (metricMergeFile, error) {
	var data metricMergeFile
	file, err := os.Open(t.metricMergePath())
	if os.IsNotExist(err) {
		return data, nil
	}
	if err != nil {
		return data, fmt.Errorf("open metric merge store: %w", err)
	}
	defer file.Close()
	if err := json.NewDecoder(file).Decode(&data); err != nil {
		return data, fmt.Errorf("decode metric merge store: %w", err)
	}
	return data, nil
}

func (t *MemoTool) saveMetricMerges(data metricMergeFile) error {
	return atomicWriteJSON(t.metricMergePath(), data)
}

// MetricMergeSuggestions returns every pending suggestion, oldest first,
// for the web UI's approval queue.
func (t *MemoTool) MetricMergeSuggestions() ([]MetricMergeSuggestion, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	data, err := t.loadMetricMerges()
	if err != nil {
		return nil, err
	}
	pending := make([]MetricMergeSuggestion, 0, len(data.Suggestions))
	for _, s := range data.Suggestions {
		if s.Status == "pending" {
			pending = append(pending, s)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ProposedAt < pending[j].ProposedAt })
	return pending, nil
}

// DecideMetricMerge resolves a pending suggestion. Approving renames
// MetricName to Canonical on every memo — active or archived — currently
// carrying any of the suggestion's Names, so the decision sticks even if a
// memo is later un-archived. Rejecting just records the decision, so
// CheckMetricMerges never proposes this exact pair again.
func (t *MemoTool) DecideMetricMerge(id string, approve bool) (MetricMergeSuggestion, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	mergeData, err := t.loadMetricMerges()
	if err != nil {
		return MetricMergeSuggestion{}, err
	}
	index := -1
	for i, s := range mergeData.Suggestions {
		if s.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		return MetricMergeSuggestion{}, fmt.Errorf("metric merge suggestion %q was not found", id)
	}
	suggestion := mergeData.Suggestions[index]
	if suggestion.Status != "pending" {
		return MetricMergeSuggestion{}, fmt.Errorf("metric merge suggestion %q was already %s", id, suggestion.Status)
	}

	if approve {
		data, err := t.load()
		if err != nil {
			return MetricMergeSuggestion{}, err
		}
		oldNames := make(map[string]bool, len(suggestion.Names))
		for _, name := range suggestion.Names {
			oldNames[name] = true
		}
		renamed := 0
		for key, record := range data.Memos {
			if oldNames[record.MetricName] && record.MetricName != suggestion.Canonical {
				record.MetricName = suggestion.Canonical
				data.Memos[key] = record
				renamed++
			}
		}
		if renamed > 0 {
			if err := t.save(data); err != nil {
				return MetricMergeSuggestion{}, err
			}
		}
		suggestion.Status = "approved"
	} else {
		suggestion.Status = "rejected"
	}
	suggestion.DecidedAt = time.Now().Format(time.RFC3339)
	mergeData.Suggestions[index] = suggestion
	if err := t.saveMetricMerges(mergeData); err != nil {
		return MetricMergeSuggestion{}, err
	}
	return suggestion, nil
}

// mergeLineRE matches one line of CheckMetricMerges' expected response
// format: "N: yes, canonical_name" or "N: no".
var mergeLineRE = regexp.MustCompile(`(?i)^\s*(\d+)\s*:\s*(yes|no)\s*(?:,\s*(.+?))?\s*$`)

// CheckMetricMerges looks for pairs of known metric names (see
// maintenance's known_metrics) that might be the same physical counter
// under two different spellings — a fragmentation a weak local model can
// still introduce despite write's existing_metric_names hint. It never
// merges anything itself: a plausible pair becomes a pending suggestion
// (see MetricMergeSuggestions/DecideMetricMerge) a human approves or
// rejects in the web UI, so a wrong guess only ever costs one item to
// dismiss, never lost or conflated data. One batched LLM call per run,
// like NormalizeTags, and a plain-text response rather than a tool call —
// it doesn't depend on the local model's tool-calling reliability, only
// its ability to answer a plain question. limit caps how many new
// candidate pairs are checked in one call. Returns how many new
// suggestions were added.
func (t *MemoTool) CheckMetricMerges(ctx context.Context, client chatClient, limit int) (int, error) {
	if client == nil || limit <= 0 {
		return 0, nil
	}

	t.mu.Lock()
	data, err := t.load()
	if err != nil {
		t.mu.Unlock()
		return 0, err
	}
	mergeData, err := t.loadMetricMerges()
	if err != nil {
		t.mu.Unlock()
		return 0, err
	}
	t.mu.Unlock()

	latestContent := make(map[string]string)
	latestAt := make(map[string]string)
	for _, record := range data.Memos {
		if record.Status == "archived" || record.MetricName == "" {
			continue
		}
		if record.UpdatedAt > latestAt[record.MetricName] {
			latestAt[record.MetricName] = record.UpdatedAt
			latestContent[record.MetricName] = record.Content
		}
	}
	names := make([]string, 0, len(latestAt))
	for name := range latestAt {
		names = append(names, name)
	}
	sort.Strings(names)

	decided := make(map[string]bool, len(mergeData.Suggestions))
	for _, s := range mergeData.Suggestions {
		decided[s.ID] = true
	}

	type candidatePair struct {
		id   string
		a, b string
	}
	var candidates []candidatePair
outer:
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			id := metricMergeID(names[i], names[j])
			if decided[id] {
				continue
			}
			candidates = append(candidates, candidatePair{id, names[i], names[j]})
			if len(candidates) >= limit {
				break outer
			}
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	var prompt strings.Builder
	prompt.WriteString("Each numbered pair below is two counter names tracked separately for equipment upkeep (like a car's odometer or a boat's engine-hour meter), with the latest note recorded against each. Decide whether each pair is plausibly the SAME physical counter under two different names (a naming mistake) rather than two different pieces of equipment. For each pair output one line: \"N: yes, <short_canonical_name>\" (a clear name to merge both onto) if it's the same counter, or \"N: no\" if these are genuinely different. Output only these lines, nothing else — no explanations.\n\n")
	for i, c := range candidates {
		fmt.Fprintf(&prompt, "%d. %q (latest: %q) vs %q (latest: %q)\n", i+1, c.a, latestContent[c.a], c.b, latestContent[c.b])
	}

	response, err := client.Chat(ctx, []llm.Message{{Role: "user", Content: prompt.String()}}, nil)
	if err != nil {
		return 0, fmt.Errorf("check metric merges: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	proposed := make(map[string]MetricMergeSuggestion)
	for _, line := range strings.Split(response.Content, "\n") {
		match := mergeLineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil || index < 1 || index > len(candidates) {
			continue
		}
		if !strings.EqualFold(match[2], "yes") {
			continue
		}
		canonical := strings.TrimSpace(match[3])
		if canonical == "" {
			continue
		}
		c := candidates[index-1]
		proposed[c.id] = MetricMergeSuggestion{
			ID: c.id, Names: []string{c.a, c.b}, Canonical: canonical,
			Status: "pending", ProposedAt: now,
		}
	}
	if len(proposed) == 0 {
		return 0, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	mergeData, err = t.loadMetricMerges()
	if err != nil {
		return 0, err
	}
	existing := make(map[string]bool, len(mergeData.Suggestions))
	for _, s := range mergeData.Suggestions {
		existing[s.ID] = true
	}
	added := 0
	for _, s := range proposed {
		if existing[s.ID] {
			continue // proposed or decided by a concurrent run since the check above
		}
		mergeData.Suggestions = append(mergeData.Suggestions, s)
		added++
	}
	if added > 0 {
		if err := t.saveMetricMerges(mergeData); err != nil {
			return 0, err
		}
	}
	return added, nil
}
