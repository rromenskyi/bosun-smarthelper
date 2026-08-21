package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestStoreInsertAndQuery(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	for i := 0; i < 5; i++ {
		if err := store.Insert(ctx, base.Add(time.Duration(i)*time.Minute), "cpu_temp_c", float64(60+i)); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	points, err := store.Query(ctx, "cpu_temp_c", base.Add(-time.Minute), 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(points) != 5 {
		t.Fatalf("len(points) = %d, want 5", len(points))
	}
	for i, p := range points {
		if p.Value != float64(60+i) {
			t.Errorf("points[%d].Value = %v, want %v", i, p.Value, float64(60+i))
		}
	}
}

func TestStoreQueryBucketsWhenExceedingMaxPoints(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	base := time.Now().Add(-100 * time.Second)

	// 100 one-second samples, alternating 0/100 so a naive average isn't
	// just "the same value repeated" — asking for at most 10 points must
	// merge ~10 raw samples per bucket without losing them entirely.
	for i := 0; i < 100; i++ {
		value := 0.0
		if i%2 == 1 {
			value = 100.0
		}
		if err := store.Insert(ctx, base.Add(time.Duration(i)*time.Second), "cpu_percent", value); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	points, err := store.Query(ctx, "cpu_percent", base, 10)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("Query returned no points")
	}
	if len(points) > 10 {
		t.Errorf("len(points) = %d, want at most 10", len(points))
	}
	var sum float64
	for _, p := range points {
		sum += p.Value
	}
	// Individual buckets (especially the boundary ones) can skew away from
	// 50 depending on exactly how many of each alternating value land in
	// them — what matters is that bucketing actually averaged instead of
	// e.g. only keeping every 10th raw sample, which the overall mean
	// across all buckets would expose (it'd land near 0 or 100, not ~50).
	if mean := sum / float64(len(points)); mean < 40 || mean > 60 {
		t.Errorf("mean of bucket averages = %v, want roughly 50 (averaging alternating 0/100)", mean)
	}
}

func TestStoreQueryUnknownMetricReturnsEmpty(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.Insert(ctx, time.Now(), "cpu_temp_c", 60); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	points, err := store.Query(ctx, "does_not_exist", time.Now().Add(-time.Hour), 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(points) != 0 {
		t.Errorf("len(points) = %d, want 0", len(points))
	}
}

func TestStorePrune(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now()

	if err := store.Insert(ctx, now.Add(-48*time.Hour), "cpu_temp_c", 60); err != nil {
		t.Fatalf("Insert old: %v", err)
	}
	if err := store.Insert(ctx, now, "cpu_temp_c", 70); err != nil {
		t.Fatalf("Insert recent: %v", err)
	}

	if err := store.Prune(ctx, now.Add(-24*time.Hour)); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	points, err := store.Query(ctx, "cpu_temp_c", now.Add(-72*time.Hour), 0)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("len(points) = %d, want 1 (the pruned sample should be gone)", len(points))
	}
	if points[0].Value != 70 {
		t.Errorf("points[0].Value = %v, want 70 (the surviving, recent sample)", points[0].Value)
	}
}

func TestStoreMetricsListsDistinctNames(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, m := range []string{"cpu_temp_c", "cpu_temp_c", "mem_used_percent"} {
		if err := store.Insert(ctx, now, m, 1); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	names, err := store.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	want := []string{"cpu_temp_c", "mem_used_percent"}
	if len(names) != len(want) {
		t.Fatalf("Metrics() = %v, want %v", names, want)
	}
	for i, name := range names {
		if name != want[i] {
			t.Errorf("Metrics()[%d] = %q, want %q", i, name, want[i])
		}
	}
}
