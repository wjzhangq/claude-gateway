package db

import "fmt"

// placeholder returns a positional placeholder ($1, $2, ...) for postgres
// or "?" for sqlite, depending on the driver.
func (d *DB) placeholder(n int) string {
	if d.driver == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

// placeholders returns n placeholders separated by commas.
func (d *DB) placeholders(start, count int) string {
	if d.driver == "sqlite" {
		s := ""
		for i := 0; i < count; i++ {
			if i > 0 {
				s += ", "
			}
			s += "?"
		}
		return s
	}
	s := ""
	for i := 0; i < count; i++ {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("$%d", start+i)
	}
	return s
}

// boolVal returns the appropriate boolean representation for the driver.
func (d *DB) boolVal(b bool) interface{} {
	if d.driver == "postgres" {
		return b
	}
	if b {
		return 1
	}
	return 0
}

// boolTrue returns the SQL literal for "true" depending on driver.
func (d *DB) boolTrue() string {
	if d.driver == "postgres" {
		return "true"
	}
	return "1"
}

// isPostgres returns true if the database driver is PostgreSQL.
func (d *DB) isPostgres() bool {
	return d.driver == "postgres"
}

// dateSubstr returns the SQL expression to extract a date string from a timestamp column.
func (d *DB) dateSubstr(col string) string {
	if d.driver == "postgres" {
		return fmt.Sprintf("TO_CHAR(%s, 'YYYY-MM-DD')", col)
	}
	return fmt.Sprintf("SUBSTR(%s, 1, 10)", col)
}

// upsertConflict returns the appropriate ON CONFLICT clause.
// For postgres: ON CONFLICT (cols) DO UPDATE SET ...
// For sqlite: same syntax (supported since SQLite 3.24.0)
func (d *DB) upsertConflict() string {
	return "ON CONFLICT"
}

// nowFunc returns the SQL function for current timestamp.
func (d *DB) nowFunc() string {
	if d.driver == "postgres" {
		return "NOW()"
	}
	return "CURRENT_TIMESTAMP"
}
