package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/roman220/bosun-smarthelper/internal/llm"
)

// tagLineRE matches one line of NormalizeTags' expected response format:
// "<candidate index>: tag1, tag2" or "<candidate index>: none".
var tagLineRE = regexp.MustCompile(`^\s*(\d+)\s*:\s*(.+?)\s*$`)

// chatClient is the minimal capability NormalizeTags needs — matches
// agent.ChatClient's shape so *llm.Router satisfies it directly, without
// requiring the full llm.Client interface (Model/Provider aren't used
// here).
type chatClient interface {
	Chat(ctx context.Context, messages []llm.Message, tools []llm.ToolDefinition) (*llm.Response, error)
}

// NormalizeTags maps a batch of memos' free-form Tags onto canonicalTags
// with one LLM call (batched, not one call per memo, to stay cheap — see
// docs/memo-search.md). It is additive and best-effort: CanonicalTags is
// only ever added to, never replacing Tags, and any memo the model's
// response doesn't clearly cover is simply left for the next call rather
// than treated as an error. Returns how many memos were updated.
func (t *MemoTool) NormalizeTags(ctx context.Context, client chatClient, canonicalTags []string, limit int) (int, error) {
	if len(canonicalTags) == 0 || client == nil {
		return 0, nil
	}

	t.mu.Lock()
	data, err := t.load()
	if err != nil {
		t.mu.Unlock()
		return 0, err
	}

	type candidate struct {
		key  string
		tags []string
	}
	var candidates []candidate
	for _, record := range data.Memos {
		if len(record.Tags) == 0 {
			continue
		}
		// A plain string comparison, so both timestamps must share the
		// same precision — UpdatedAt is RFC3339Nano (see memo.go), so
		// TagsNormalizedAt is stamped the same way below; whole-second
		// RFC3339 sorts before an otherwise-identical-second RFC3339Nano
		// string (".123..." > "-06:00"'s leading "-"), which would make
		// an up-to-date memo look perpetually stale and get re-normalized
		// forever.
		if record.TagsNormalizedAt != "" && record.TagsNormalizedAt >= record.UpdatedAt {
			continue
		}
		candidates = append(candidates, candidate{key: record.Key, tags: record.Tags})
		if len(candidates) >= limit {
			break
		}
	}
	t.mu.Unlock()
	if len(candidates) == 0 {
		return 0, nil
	}

	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Canonical categories: %s\n\n", strings.Join(canonicalTags, ", "))
	prompt.WriteString("For each numbered note below, output one line \"N: category, category\" listing every canonical category (from the list above, exactly as spelled) that fits its tags, or \"N: none\" if nothing fits well. Output only these lines, nothing else — no explanations, no extra text.\n\n")
	for i, c := range candidates {
		fmt.Fprintf(&prompt, "%d. tags: %s\n", i+1, strings.Join(c.tags, ", "))
	}

	response, err := client.Chat(ctx, []llm.Message{{Role: "user", Content: prompt.String()}}, nil)
	if err != nil {
		return 0, fmt.Errorf("normalize tags: %w", err)
	}

	canonicalSet := make(map[string]string, len(canonicalTags)) // lowercase -> canonical spelling
	for _, tag := range canonicalTags {
		canonicalSet[strings.ToLower(tag)] = tag
	}

	updates := make(map[int][]string) // candidate index -> matched canonical tags
	for _, line := range strings.Split(response.Content, "\n") {
		match := tagLineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil || index < 1 || index > len(candidates) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(match[2]), "none") {
			continue
		}
		var matched []string
		for _, raw := range strings.Split(match[2], ",") {
			if canonical, ok := canonicalSet[strings.ToLower(strings.TrimSpace(raw))]; ok {
				matched = append(matched, canonical)
			}
		}
		if len(matched) > 0 {
			updates[index-1] = matched
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	data, err = t.load()
	if err != nil {
		return 0, err
	}
	now := time.Now().Format(time.RFC3339Nano)
	updated := 0
	for index, tags := range updates {
		key := candidates[index].key
		record, ok := data.Memos[key]
		if !ok {
			continue // deleted since the batch was built
		}
		record.CanonicalTags = tags
		record.TagsNormalizedAt = now
		data.Memos[key] = record
		updated++
	}
	if updated > 0 {
		if err := t.save(data); err != nil {
			return 0, err
		}
	}
	return updated, nil
}
