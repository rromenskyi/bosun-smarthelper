package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/roman220/ai-local-smarthelper/internal/config"
)

func TestMetricMergeIDIsOrderIndependent(t *testing.T) {
	if metricMergeID("a", "b") != metricMergeID("b", "a") {
		t.Error("metricMergeID must not depend on argument order")
	}
}

func TestCheckMetricMergesProposesPlausiblePair(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "reading-1", "content": "current odometer",
		"metric_name": "odometer_miles", "metric_value": 26829.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "changed the oil",
		"metric_name": "oil_change_odometer", "metric_value": 26829.0, "due_metric_value": 28000.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	client := &fakeChatClient{response: "1: yes, odometer_miles"}
	added, err := tool.CheckMetricMerges(ctx, client, 10)
	if err != nil {
		t.Fatalf("CheckMetricMerges: %v", err)
	}
	if added != 1 {
		t.Fatalf("added = %d, want 1", added)
	}

	pending, err := tool.MetricMergeSuggestions()
	if err != nil {
		t.Fatalf("MetricMergeSuggestions: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want exactly 1", pending)
	}
	if pending[0].Canonical != "odometer_miles" || pending[0].Status != "pending" {
		t.Errorf("suggestion = %+v, want canonical odometer_miles, status pending", pending[0])
	}
	if len(pending[0].Names) != 2 {
		t.Errorf("names = %v, want both metric names", pending[0].Names)
	}
}

func TestCheckMetricMergesSkipsWhenModelSaysNo(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "car", "content": "current odometer",
		"metric_name": "odometer_km", "metric_value": 61000.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "boat", "content": "main engine hours",
		"metric_name": "main_engine_hours", "metric_value": 340.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	client := &fakeChatClient{response: "1: no"}
	added, err := tool.CheckMetricMerges(ctx, client, 10)
	if err != nil {
		t.Fatalf("CheckMetricMerges: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0 — the model said these are different equipment", added)
	}
	pending, _ := tool.MetricMergeSuggestions()
	if len(pending) != 0 {
		t.Errorf("pending = %+v, want none", pending)
	}
}

func TestCheckMetricMergesDoesNotRecheckAlreadyDecidedPair(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "a", "content": "a", "metric_name": "name_a", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "b", "content": "b", "metric_name": "name_b", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	client := &fakeChatClient{response: "1: yes, name_a"}
	if _, err := tool.CheckMetricMerges(ctx, client, 10); err != nil {
		t.Fatalf("first check: %v", err)
	}

	// Second run: even if the model were asked again and said yes to a
	// different canonical name, the already-pending pair must not gain a
	// second suggestion.
	client2 := &fakeChatClient{response: "1: yes, something_else"}
	added, err := tool.CheckMetricMerges(ctx, client2, 10)
	if err != nil {
		t.Fatalf("second check: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d on second run, want 0 — the pair already has a pending suggestion", added)
	}
	pending, _ := tool.MetricMergeSuggestions()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want still exactly 1", pending)
	}
}

func TestCheckMetricMergesWithoutClientIsNoop(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	added, err := tool.CheckMetricMerges(context.Background(), nil, 10)
	if err != nil {
		t.Fatalf("CheckMetricMerges: %v", err)
	}
	if added != 0 {
		t.Errorf("added = %d, want 0 with a nil client", added)
	}
}

func TestDecideMetricMergeApprovalRenamesMatchingMemos(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "reading", "content": "current odometer",
		"metric_name": "odometer_miles", "metric_value": 26829.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "oil-change", "content": "changed the oil",
		"metric_name": "oil_change_odometer", "metric_value": 26829.0, "due_metric_value": 28000.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	added, err := tool.CheckMetricMerges(ctx, &fakeChatClient{response: "1: yes, odometer_miles"}, 10)
	if err != nil || added != 1 {
		t.Fatalf("CheckMetricMerges: added=%d err=%v", added, err)
	}
	pending, _ := tool.MetricMergeSuggestions()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want 1", pending)
	}

	decided, err := tool.DecideMetricMerge(pending[0].ID, true)
	if err != nil {
		t.Fatalf("DecideMetricMerge: %v", err)
	}
	if decided.Status != "approved" || decided.DecidedAt == "" {
		t.Errorf("decided = %+v, want status approved with a decided_at", decided)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "maintenance"})
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	metrics, _ := view["known_metrics"].([]string)
	if len(metrics) != 1 || metrics[0] != "odometer_miles" {
		t.Errorf("known_metrics = %v, want just odometer_miles after the merge", metrics)
	}

	// No longer a pending suggestion, and re-checking must not propose it
	// again (both names funnel into the same one now anyway).
	pendingAfter, _ := tool.MetricMergeSuggestions()
	if len(pendingAfter) != 0 {
		t.Errorf("pending after approval = %+v, want none", pendingAfter)
	}
}

func TestDecideMetricMergeRejectionKeepsNamesSeparate(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()

	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "a", "content": "a", "metric_name": "name_a", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "b", "content": "b", "metric_name": "name_b", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	added, err := tool.CheckMetricMerges(ctx, &fakeChatClient{response: "1: yes, name_a"}, 10)
	if err != nil || added != 1 {
		t.Fatalf("CheckMetricMerges: added=%d err=%v", added, err)
	}
	pending, _ := tool.MetricMergeSuggestions()

	decided, err := tool.DecideMetricMerge(pending[0].ID, false)
	if err != nil {
		t.Fatalf("DecideMetricMerge: %v", err)
	}
	if decided.Status != "rejected" {
		t.Errorf("status = %q, want rejected", decided.Status)
	}

	result, err := tool.Execute(ctx, map[string]any{"action": "maintenance"})
	if err != nil {
		t.Fatalf("maintenance: %v", err)
	}
	view := result.(map[string]any)
	metrics, _ := view["known_metrics"].([]string)
	if len(metrics) != 2 {
		t.Errorf("known_metrics = %v, want both names still separate after rejection", metrics)
	}

	// Re-checking must not propose the same pair again.
	added2, err := tool.CheckMetricMerges(ctx, &fakeChatClient{response: "1: yes, name_a"}, 10)
	if err != nil {
		t.Fatalf("second CheckMetricMerges: %v", err)
	}
	if added2 != 0 {
		t.Errorf("added on recheck = %d, want 0 — this pair was already rejected", added2)
	}
}

func TestDecideMetricMergeUnknownIDErrors(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	if _, err := tool.DecideMetricMerge("does-not-exist", true); err == nil {
		t.Error("expected an error for an unknown suggestion id")
	}
}

func TestDecideMetricMergeAlreadyDecidedErrors(t *testing.T) {
	tool := NewMemoTool(&config.MemoConfig{Path: filepath.Join(t.TempDir(), "memos.json")}, nil)
	ctx := context.Background()
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "a", "content": "a", "metric_name": "name_a", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.Execute(ctx, map[string]any{
		"action": "write", "key": "b", "content": "b", "metric_name": "name_b", "metric_value": 1.0,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tool.CheckMetricMerges(ctx, &fakeChatClient{response: "1: yes, name_a"}, 10); err != nil {
		t.Fatalf("CheckMetricMerges: %v", err)
	}
	pending, _ := tool.MetricMergeSuggestions()
	if _, err := tool.DecideMetricMerge(pending[0].ID, true); err != nil {
		t.Fatalf("first decide: %v", err)
	}
	if _, err := tool.DecideMetricMerge(pending[0].ID, false); err == nil {
		t.Error("expected an error deciding an already-decided suggestion")
	}
}
