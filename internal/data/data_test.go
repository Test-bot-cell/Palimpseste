package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"palimpseste/internal/theme"
)

var equipeSchema = theme.DataTable{
	Format: "csv",
	Columns: map[string]string{
		"nom": "string", "age": "number", "actif": "bool",
		"depuis": "date", "photo": "media",
	},
}

func sample() *Table {
	return &Table{
		Name:   "equipe",
		Header: []string{"nom", "age", "actif", "depuis", "photo"},
		Rows: [][]string{
			{"Ada", "36", "true", "2024-01-15", "media/ada.webp"},
			{"Blaise", "39", "false", "2023-06-01", ""},
		},
	}
}

func TestSaveLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, sample(), equipeSchema); err != nil {
		t.Fatal(err)
	}
	got, found, err := Load(dir, "equipe")
	if err != nil || !found {
		t.Fatalf("load: %v found=%v", err, found)
	}
	if len(got.Rows) != 2 || got.Rows[0][0] != "Ada" || got.Rows[1][4] != "" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

// §3.3: re-saving unchanged data is byte-stable — the diff stays readable and
// the undo story honest.
func TestSaveIsByteStable(t *testing.T) {
	dir := t.TempDir()
	path, err := Save(dir, sample(), equipeSchema)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	got, _, _ := Load(dir, "equipe")
	if _, err := Save(dir, got, equipeSchema); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("save/load/save changed bytes:\n1: %q\n2: %q", first, second)
	}
}

// The schema is authoritative on every write (§3.3, "le bluemonday des
// données") — wrong shape or wrong values never reach disk.
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Table)
	}{
		{"missing declared column", func(t *Table) {
			t.Header = t.Header[:4]
			for i := range t.Rows {
				t.Rows[i] = t.Rows[i][:4]
			}
		}},
		{"undeclared column", func(t *Table) {
			t.Header = append(t.Header, "mdp")
			for i := range t.Rows {
				t.Rows[i] = append(t.Rows[i], "x")
			}
		}},
		{"ragged row", func(t *Table) { t.Rows[0] = t.Rows[0][:3] }},
		{"bad number", func(t *Table) { t.Rows[0][1] = "trente-six" }},
		{"bad bool", func(t *Table) { t.Rows[0][2] = "oui" }},
		{"bad date", func(t *Table) { t.Rows[0][3] = "15/01/2024" }},
		{"media outside media/", func(t *Table) { t.Rows[0][4] = "../../etc/passwd" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tab := sample()
			c.mutate(tab)
			if err := Validate(tab, equipeSchema); err == nil {
				t.Error("hostile table passed validation")
			}
		})
	}
}

func TestEmptyCellsAreData(t *testing.T) {
	tab := sample()
	tab.Rows[0][1], tab.Rows[0][3] = "", ""
	if err := Validate(tab, equipeSchema); err != nil {
		t.Errorf("empty cells must validate: %v", err)
	}
}

func TestMediaCellsCanonicalised(t *testing.T) {
	dir := t.TempDir()
	tab := sample()
	tab.Rows[0][4] = "/media/ada.webp" // root-relative spelling
	path, err := Save(dir, tab, equipeSchema)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "media/ada.webp") || strings.Contains(string(b), "/media/ada.webp") {
		t.Errorf("media cell not canonicalised: %s", b)
	}
}

func TestPathConfined(t *testing.T) {
	for _, bad := range []string{"..", "a/b", "A", "-x", "équipe"} {
		if _, err := Path(t.TempDir(), bad); err == nil {
			t.Errorf("table name %q accepted", bad)
		}
	}
	p, err := Path("/site", "equipe_2026")
	if err != nil || p != filepath.Join("/site", "data", "equipe_2026.csv") {
		t.Errorf("Path = %q, %v", p, err)
	}
}
