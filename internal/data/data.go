// Package data implements the data contract (§3.3): the tabular truth of a
// site lives as flat, diffable, committable files under data/ — CSV for the
// table spirit — and every write is validated authoritatively server-side
// against the schema the active theme declares (§5.1 "data"): the bluemonday
// of data. The query engine stays a build detail: at site scale a table is a
// slice in memory; no embedded database, ever (§3.3).
package data

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"palimpseste/internal/sanitize"
	"palimpseste/internal/theme"
)

// nameRE pins table identifiers to the §14 discipline: strict shape, traversal
// unspellable. It matches the blocks catalogue's data-source parameter.
var nameRE = regexp.MustCompile(`^[a-z0-9]+(?:[_-][a-z0-9]+)*$`)

// Table is one loaded table: a header and its rows, in file order.
type Table struct {
	Name   string     `json:"name"`
	Header []string   `json:"header"`
	Rows   [][]string `json:"rows"`
}

// ValidName reports whether name is an admissible table identifier.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// Path resolves a table name to its CSV file under <siteDir>/data.
func Path(siteDir, name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("invalid table name %q", name)
	}
	return filepath.Join(siteDir, "data", name+".csv"), nil
}

// Load reads data/<name>.csv. A missing file is not an error: found is false
// and the caller decides what an absent table means (an empty grid for the
// editor, a skipped block for the materializer).
func Load(siteDir, name string) (t *Table, found bool, err error) {
	path, err := Path(siteDir, name)
	if err != nil {
		return nil, false, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("open table %q: %w", name, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // validated against the schema, not the reader
	records, err := r.ReadAll()
	if err != nil {
		return nil, false, fmt.Errorf("parse table %q: %w", name, err)
	}
	if len(records) == 0 {
		return &Table{Name: name}, true, nil
	}
	return &Table{Name: name, Header: records[0], Rows: records[1:]}, true, nil
}

// Validate checks a table against its declared schema (§3.3): every declared
// column present exactly once, no undeclared column, every cell admissible for
// its column type. Column order is the file's own (a human choice, preserved);
// the schema constrains names and values, not order.
func Validate(t *Table, schema theme.DataTable) error {
	seen := map[string]int{}
	for _, h := range t.Header {
		seen[h]++
	}
	for col := range schema.Columns {
		switch seen[col] {
		case 0:
			return fmt.Errorf("column %q declared by the schema is missing", col)
		case 1:
		default:
			return fmt.Errorf("column %q appears %d times", col, seen[col])
		}
	}
	for _, h := range t.Header {
		if _, ok := schema.Columns[h]; !ok {
			return fmt.Errorf("column %q is not declared by the schema", h)
		}
	}

	for i, row := range t.Rows {
		if len(row) != len(t.Header) {
			return fmt.Errorf("row %d has %d cells, header has %d", i+1, len(row), len(t.Header))
		}
		for j, cell := range row {
			col := t.Header[j]
			if err := validCell(cell, schema.Columns[col]); err != nil {
				return fmt.Errorf("row %d, column %q: %w", i+1, col, err)
			}
		}
	}
	return nil
}

// validCell admits one cell for a column type. Empty cells are permitted for
// every type — absence is data too; the §11 lint is where "should" lives.
func validCell(cell, colType string) error {
	if cell == "" {
		return nil
	}
	switch colType {
	case "string":
		return nil
	case "number":
		if _, err := strconv.ParseFloat(cell, 64); err != nil {
			return fmt.Errorf("%q is not a number", cell)
		}
	case "bool":
		if cell != "true" && cell != "false" {
			return fmt.Errorf("%q is not true/false", cell)
		}
	case "date":
		if _, err := time.Parse("2006-01-02", cell); err != nil {
			return fmt.Errorf("%q is not an ISO date (AAAA-MM-JJ)", cell)
		}
	case "media":
		if _, ok := sanitize.CanonicalMediaSrc(cell); !ok {
			return fmt.Errorf("%q is not a media/ path", cell)
		}
	default:
		return fmt.Errorf("unknown column type %q", colType)
	}
	return nil
}

// Save validates the table against schema and writes data/<name>.csv
// atomically, in canonical CSV (RFC 4180 quoting as encoding/csv emits it, one
// trailing newline) so re-saving unchanged data is byte-stable and diffs stay
// line-per-row readable (§3.3). media cells are stored in canonical media/
// form, mirroring the content contract.
func Save(siteDir string, t *Table, schema theme.DataTable) (string, error) {
	if err := Validate(t, schema); err != nil {
		return "", err
	}
	path, err := Path(siteDir, t.Name)
	if err != nil {
		return "", err
	}

	// Canonicalise media cells before writing.
	for j, col := range t.Header {
		if schema.Columns[col] != "media" {
			continue
		}
		for _, row := range t.Rows {
			if row[j] == "" {
				continue
			}
			canon, _ := sanitize.CanonicalMediaSrc(row[j])
			row[j] = canon
		}
	}

	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write(t.Header); err != nil {
		return "", err
	}
	if err := w.WriteAll(t.Rows); err != nil {
		return "", err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pal-data-*.tmp")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return path, nil
}
