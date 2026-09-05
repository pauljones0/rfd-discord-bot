package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type schemaReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type schemaColumn struct {
	name       string
	typeName   string
	notNull    int
	primaryKey int
}

var storeSchema = map[string][]schemaColumn{
	"deals": {
		{"id", "TEXT", 0, 1}, {"payload", "TEXT", 1, 0},
		{"published_at", "INTEGER", 1, 0}, {"updated_at", "INTEGER", 1, 0},
	},
	"subscriptions": {
		{"guild_id", "TEXT", 1, 1}, {"channel_id", "TEXT", 1, 2},
		{"filter", "TEXT", 1, 3}, {"payload", "TEXT", 1, 0},
	},
	"settings": {{"key", "TEXT", 0, 1}, {"payload", "TEXT", 1, 0}},
}

// validateStoreSchema performs reads only. Accept missing tables for a new
// database, but reject any existing table that belongs to a different schema.
func validateStoreSchema(ctx context.Context, reader schemaReader) error {
	rows, err := reader.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT GLOB 'sqlite_*' ORDER BY name")
	if err != nil {
		return fmt.Errorf("inspect destination database: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, name := range names {
		expected, exists := storeSchema[name]
		if !exists {
			return fmt.Errorf("destination is not a standalone RFD database: foreign table %q; choose a new database path", name)
		}
		// name is one of the fixed keys above, never arbitrary SQL input.
		columns, err := reader.QueryContext(ctx, `PRAGMA table_xinfo("`+name+`")`)
		if err != nil {
			return err
		}
		position := 0
		compatible := true
		for columns.Next() {
			var column schemaColumn
			var cid, hidden int
			var defaultValue sql.NullString
			if err = columns.Scan(&cid, &column.name, &column.typeName, &column.notNull, &defaultValue, &column.primaryKey, &hidden); err != nil {
				columns.Close()
				return err
			}
			column.typeName = strings.ToUpper(strings.TrimSpace(column.typeName))
			if position >= len(expected) || column != expected[position] || defaultValue.Valid || hidden != 0 {
				compatible = false
			}
			position++
		}
		if err = columns.Err(); err != nil {
			columns.Close()
			return err
		}
		columns.Close()
		if !compatible || position != len(expected) {
			return fmt.Errorf("destination table %q has an incompatible schema; choose a new database path", name)
		}
	}
	return nil
}
