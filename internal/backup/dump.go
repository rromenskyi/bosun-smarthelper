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
// database while internal/metrics.Store (a separate OS process, since
// this only ever runs from the standalone `smarthelper backup` command)
// has it open too, the same as any two ordinary SQLite readers on one
// file.
func DumpSQL(path string) ([]byte, error) {
	db, err := sql.Open("sqlite", path)
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
