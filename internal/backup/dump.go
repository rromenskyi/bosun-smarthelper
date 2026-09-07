package backup

import (
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// DumpSQL renders every row of a SQLite database (schema-agnostic — walks
// sqlite_master rather than assuming a fixed table list, so it keeps
// working if internal/metrics' schema ever grows) as portable SQL text
// (CREATE TABLE + INSERT statements), rather than copying the raw .db
// file. A plain file copy risks a torn, inconsistent snapshot if a write
// lands mid-copy; a SQL dump reads through the same transactional API any
// other query would, and is restorable across SQLite versions without
// needing the exact same on-disk page format.
//
// Opens its own read connection to path — safe to run against the live
// database while internal/metrics.Store has it open too, whether that's
// a separate process (the standalone `smarthelper backup` command) or
// the same one (runBackupScheduler's automatic, in-process schedule).
// Relies on metrics.Store having put the database in WAL mode (a
// persistent, file-level setting, so it's already in effect regardless
// of which of those wrote it) so a read here never blocks on or blocks
// an in-progress write; _busy_timeout is one more layer of defense
// (waits out a lock instead of failing immediately) for anything WAL
// alone doesn't cover, e.g. a checkpoint in progress.
func DumpSQL(path string) ([]byte, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	defer db.Close()

	tables, err := tableSchemas(db)
	if err != nil {
		return nil, err
	}

	var out strings.Builder
	out.WriteString("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\n")
	for _, table := range tables {
		fmt.Fprintf(&out, "%s;\n", table.createSQL)
		if err := dumpRows(db, table.name, &out); err != nil {
			return nil, err
		}
	}
	out.WriteString("COMMIT;\n")
	return []byte(out.String()), nil
}

type tableSchema struct {
	name      string
	createSQL string
}

func tableSchemas(db *sql.DB) ([]tableSchema, error) {
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var tables []tableSchema
	for rows.Next() {
		var t tableSchema
		if err := rows.Scan(&t.name, &t.createSQL); err != nil {
			return nil, fmt.Errorf("scan table schema: %w", err)
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

func dumpRows(db *sql.DB, table string, out *strings.Builder) error {
	rows, err := db.Query(fmt.Sprintf("SELECT * FROM %q", table))
	if err != nil {
		return fmt.Errorf("select from %s: %w", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("read columns for %s: %w", table, err)
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return fmt.Errorf("scan row from %s: %w", table, err)
		}
		fmt.Fprintf(out, "INSERT INTO %q VALUES (%s);\n", table, sqlLiterals(values))
	}
	return rows.Err()
}

func sqlLiterals(values []any) string {
	parts := make([]string, len(values))
	for i, v := range values {
		switch val := v.(type) {
		case nil:
			parts[i] = "NULL"
		case int64:
			parts[i] = fmt.Sprintf("%d", val)
		case float64:
			parts[i] = fmt.Sprintf("%v", val)
		case []byte:
			parts[i] = "'" + strings.ReplaceAll(string(val), "'", "''") + "'"
		case string:
			parts[i] = "'" + strings.ReplaceAll(val, "'", "''") + "'"
		default:
			parts[i] = fmt.Sprintf("'%v'", val)
		}
	}
	return strings.Join(parts, ", ")
}
