package build

import (
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden output under testdata/golden")

// TestBuildGolden builds the example Drake site and compares the whole output
// tree to committed goldens. Run with -update to regenerate after intentional
// changes: go test ./internal/build -run TestBuildGolden -update
func TestBuildGolden(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	out := filepath.Join(t.TempDir(), "dist")
	res, err := Run(Options{
		SiteDir: "../../examples/drake",
		OutDir:  out,
		Check:   true,
		Version: "test",
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Pages != 2 {
		t.Errorf("pages = %d, want 2", res.Pages)
	}
	if len(res.Assets) != 1 {
		t.Errorf("assets = %d, want 1", len(res.Assets))
	}
	if len(res.Issues) != 1 || res.Issues[0].Rule != "missing-slot" || res.Issues[0].Page != "about" {
		t.Errorf("issues = %+v, want one about/missing-slot", res.Issues)
	}

	const goldenDir = "testdata/golden"
	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		copyTree(t, out, goldenDir)
		t.Logf("updated goldens in %s", goldenDir)
	}

	want := relFiles(t, goldenDir)
	got := relFiles(t, out)

	for rel, wb := range want {
		gb, ok := got[rel]
		if !ok {
			t.Errorf("missing output file %q", rel)
			continue
		}
		if string(gb) != string(wb) {
			t.Errorf("file %q differs from golden", rel)
		}
	}
	for rel := range got {
		if _, ok := want[rel]; !ok {
			t.Errorf("unexpected output file %q (not in golden)", rel)
		}
	}
}

// TestBuildDeterministic builds twice and asserts byte-identical trees.
func TestBuildDeterministic(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	for _, d := range []string{dirA, dirB} {
		if _, err := Run(Options{SiteDir: "../../examples/drake", OutDir: d, Version: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := relFiles(t, dirA), relFiles(t, dirB)
	if len(a) != len(b) {
		t.Fatalf("file counts differ: %d vs %d", len(a), len(b))
	}
	for rel, ab := range a {
		if string(ab) != string(b[rel]) {
			t.Errorf("file %q differs between builds", rel)
		}
	}
}

func relFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	for rel, b := range relFiles(t, src) {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
