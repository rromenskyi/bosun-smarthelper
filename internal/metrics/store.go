// Package metrics is a small local time-series store — a personal-appliance
// analog to MRTG/Grafana: sample a handful of sensors on an interval, keep a
// bounded history, and let the web UI chart them. No external service, no
// network dependency; a single SQLite file (pure-Go driver, no cgo — see
// docs/monitoring.md for why that matters on this host).
package metrics

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultPath returns the default metrics store location, mirroring the
// memo/settings/error-log convention (~/.local/share/bosun/...).
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "metrics.db"
	}
	return filepath.Join(home, ".local", "share", "bosun", "metrics.db")
}

// Point is one sample in a queried series.
type Point struct {
	Time  time.Time `json:"t"`
	Value float64   `json:"v"`
}

// Store persists metric samples to a local SQLite file.
type Store struct {
	db *sql.DB
}

// Open creates or opens the SQLite database at path, creating the schema
// (and any missing parent directory) if needed. An empty path resolves to
// DefaultPath().
func Open(path string) (*Store, error) {
	if path == "" {
		path = DefaultPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create metrics store directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open metrics store: %w", err)
	}
	// A single writer goroutine (the collector) plus occasional readers
	// (API queries) — one connection avoids SQLite's "database is locked"
	// errors under concurrent writers without needing WAL-mode tuning for
	// what is, at most, a few samples every 30s.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS samples (
			ts     INTEGER NOT NULL,
			metric TEXT    NOT NULL,
			value  REAL    NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_samples_metric_ts ON samples(metric, ts);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create metrics schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Insert records one sample. Errors are the caller's to decide whether to
// log — a single failed write to a metrics store is never worth surfacing
// to a user.
func (s *Store) Insert(ctx context.Context, ts time.Time, metric string, value float64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO samples (ts, metric, value) VALUES (?, ?, ?)`,
		ts.Unix(), metric, value)
	return err
}

// Prune deletes every sample older than before, bounding the store's size —
// this is what keeps it MRTG-like (fixed retention) instead of growing
// forever.
func (s *Store) Prune(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM samples WHERE ts < ?`, before.Unix())
	return err
}

// Metrics returns the distinct metric names that currently have at least one
// sample — so the UI can offer only metrics a sensor has actually reported,
// not ones that exist in code but have no hardware wired up yet.
func (s *Store) Metrics(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT metric FROM samples ORDER BY metric`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Latest returns the single most recent raw sample for metric — used by
// internal/alerts' threshold checker, which needs the actual last reading,
// not Query's bucket-averaged view (right for charting a range, wrong for
// "has this one value crossed a line right now"). ok is false if metric
// has no samples at all yet.
func (s *Store) Latest(ctx context.Context, metric string) (point Point, ok bool, err error) {
	row := s.db.QueryRowContext(ctx, `SELECT ts, value FROM samples WHERE metric = ? ORDER BY ts DESC LIMIT 1`, metric)
	var ts int64
	if err := row.Scan(&ts, &point.Value); err != nil {
		if err == sql.ErrNoRows {
			return Point{}, false, nil
		}
		return Point{}, false, fmt.Errorf("query latest sample for %s: %w", metric, err)
	}
	point.Time = time.Unix(ts, 0)
	return point, true, nil
}

// RecentValues returns up to n most-recent raw sample values for metric,
// newest first — used for internal/alerts' moving-average smoothing,
// which needs actual recent raw readings, the same reason Latest exists
// instead of Query's bucket-averaged view. Fewer than n samples (or none)
// is not an error — the caller averages whatever it got.
func (s *Store) RecentValues(ctx context.Context, metric string, n int) ([]float64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT value FROM samples WHERE metric = ? ORDER BY ts DESC LIMIT ?`, metric, n)
	if err != nil {
		return nil, fmt.Errorf("query recent samples for %s: %w", metric, err)
	}
	defer rows.Close()
	var values []float64
	for rows.Next() {
		var value float64
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan recent sample for %s: %w", metric, err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// Query returns up to maxPoints points for metric since the given time,
// averaging raw samples into evenly-spaced buckets when the raw resolution
// would exceed maxPoints — a 30-day range at a 30s sample interval is ~86k
// raw rows; returning all of them for every chart render is pure waste, so
// this keeps responses (and rendering) cheap regardless of range. maxPoints
// <= 0 disables bucketing and returns raw samples.
func (s *Store) Query(ctx context.Context, metric string, since time.Time, maxPoints int) ([]Point, error) {
	bucketSeconds := int64(1)
	if maxPoints > 0 {
		rangeSeconds := time.Since(since).Seconds()
		if rangeSeconds > 0 {
			bucketSeconds = int64(math.Ceil(rangeSeconds / float64(maxPoints)))
			if bucketSeconds < 1 {
				bucketSeconds = 1
			}
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT (ts / ?) * ? AS bucket, AVG(value)
		FROM samples
		WHERE metric = ? AND ts >= ?
		GROUP BY bucket
		ORDER BY bucket
	`, bucketSeconds, bucketSeconds, metric, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("query metrics: %w", err)
	}
	defer rows.Close()

	var points []Point
	for rows.Next() {
		var bucket int64
		var value float64
		if err := rows.Scan(&bucket, &value); err != nil {
			return nil, err
		}
		points = append(points, Point{Time: time.Unix(bucket, 0), Value: value})
	}
	return points, rows.Err()
}
