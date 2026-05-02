package db

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TableSchema describes one table's structure.
type TableSchema struct {
	Name    string       `json:"name"`
	Columns []ColumnInfo `json:"columns"`
}

// ColumnInfo describes a single column.
type ColumnInfo struct {
	CID        int    `json:"cid"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	NotNull    bool   `json:"not_null"`
	Default    *string `json:"default"`
	PrimaryKey bool   `json:"primary_key"`
}

// QueryResult holds the result of a read-only SQL query.
type QueryResult struct {
	Columns  []string        `json:"columns"`
	Rows     [][]interface{} `json:"rows"`
	RowCount int             `json:"row_count"`
	Duration string          `json:"duration"`
}

// GetSchema returns the schema of all tables in the database.
func (d *DB) GetSchema() ([]TableSchema, error) {
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}

	var result []TableSchema
	for _, tbl := range tables {
		cols, err := d.getTableColumns(tbl)
		if err != nil {
			return nil, fmt.Errorf("table %s: %w", tbl, err)
		}
		result = append(result, TableSchema{Name: tbl, Columns: cols})
	}
	return result, nil
}

func (d *DB) getTableColumns(table string) ([]ColumnInfo, error) {
	// table name is from sqlite_master, safe to interpolate
	rows, err := d.Query(fmt.Sprintf("PRAGMA table_info('%s')", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cols []ColumnInfo
	for rows.Next() {
		var ci ColumnInfo
		var dflt *string
		var notNull int
		var pk int
		if err := rows.Scan(&ci.CID, &ci.Name, &ci.Type, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		ci.NotNull = notNull == 1
		ci.PrimaryKey = pk == 1
		ci.Default = dflt
		cols = append(cols, ci)
	}
	return cols, nil
}

const (
	maxQueryLimit   = 10000
	queryTimeout    = 30 * time.Second
)

var (
	// Match SELECT or PRAGMA statements (case-insensitive, leading whitespace allowed)
	readOnlyPattern = regexp.MustCompile(`(?i)^\s*(SELECT|PRAGMA)\s`)
	// Match LIMIT clause at the end
	limitPattern = regexp.MustCompile(`(?i)\bLIMIT\s+(\d+)\s*$`)
)

// ExecuteReadQuery runs a read-only SQL query with enforced LIMIT and timeout.
func (d *DB) ExecuteReadQuery(sql string) (*QueryResult, error) {
	// Validate: must be SELECT or PRAGMA
	if !readOnlyPattern.MatchString(sql) {
		return nil, fmt.Errorf("only SELECT and PRAGMA statements are allowed")
	}

	// Enforce LIMIT cap
	sql = enforceLimit(sql, maxQueryLimit)

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	start := time.Now()

	rows, err := d.QueryContext(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns: %w", err)
	}

	var result [][]interface{}
	for rows.Next() {
		values := make([]interface{}, len(columns))
		ptrs := make([]interface{}, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		// Convert []byte to string for JSON serialization
		row := make([]interface{}, len(columns))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			default:
				row[i] = val
			}
		}
		result = append(result, row)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}

	duration := time.Since(start)

	return &QueryResult{
		Columns:  columns,
		Rows:     result,
		RowCount: len(result),
		Duration: duration.String(),
	}, nil
}

// enforceLimit ensures the SQL has a LIMIT clause not exceeding maxLimit.
func enforceLimit(sql string, maxLimit int) string {
	sql = strings.TrimRight(sql, "; \t\n\r")

	if matches := limitPattern.FindStringSubmatch(sql); len(matches) == 2 {
		n, err := strconv.Atoi(matches[1])
		if err == nil && n > maxLimit {
			// Replace with maxLimit
			sql = limitPattern.ReplaceAllString(sql, fmt.Sprintf("LIMIT %d", maxLimit))
		}
		return sql
	}

	// No LIMIT found, append one
	return sql + fmt.Sprintf(" LIMIT %d", maxLimit)
}
