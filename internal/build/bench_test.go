package build

import (
	"path/filepath"
	"testing"
	"time"

	"palimpseste/internal/content"
	"palimpseste/internal/css"
	"palimpseste/internal/site"
	"palimpseste/internal/theme"
)

// §15 — budgets tenus par des tests, échec CI si dépassés.
//
// Benchmarks feed benchstat in CI (`go test -bench=. -count=10 ./... | benchstat -`)
// to catch drift; the TestBudget* functions are the hard gates: they measure the
// best of a few runs — the honest machine-speed floor, robust to a noisy CI
// neighbour — against the doc's ceilings.

// goldenSite loads the example Drake site — the same fixture the byte-exact
// golden test builds; it is the most representative input we have.
func goldenSite(b testing.TB) (string, *site.Site, *theme.Theme, *content.Loader) {
	dir := filepath.Join("..", "..", "examples", "drake")
	s, err := site.Load(dir)
	if err != nil {
		b.Fatal(err)
	}
	t, err := theme.Load(dir, s.Theme)
	if err != nil {
		b.Fatal(err)
	}
	return dir, s, t, content.NewLoader(dir)
}

func BenchmarkMaterializePage(b *testing.B) {
	_, s, t, ldr := goldenSite(b)
	p := s.SortedPages()[0]
	b.ReportAllocs()
	for b.Loop() {
		if _, _, _, err := materializeRender(t, ldr, s, p, "/assets/x.css"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBuildFull(b *testing.B) {
	dir, _, _, _ := goldenSite(b)
	out := b.TempDir()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Run(Options{SiteDir: dir, OutDir: filepath.Join(out, "o"), Version: "bench"}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCSSPass(b *testing.B) {
	_, _, t, _ := goldenSite(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := css.BuildTheme(t); err != nil {
			b.Fatal(err)
		}
	}
}

// bestOf returns the fastest of n runs of fn — the machine's demonstrated
// capability, which is what a budget bounds (a loaded CI box can be slow, but
// it cannot be faster than the code allows).
func bestOf(n int, fn func()) time.Duration {
	best := time.Duration(1<<63 - 1)
	for i := 0; i < n; i++ {
		start := time.Now()
		fn()
		if d := time.Since(start); d < best {
			best = d
		}
	}
	return best
}

// §15: "Matérialisation complète — classe ~1 ms/page". The gate allows 10× the
// class before failing: a regression that big is structural, not noise.
func TestBudgetMaterializationPerPage(t *testing.T) {
	_, s, tm, ldr := goldenSite(t)
	p := s.SortedPages()[0]
	best := bestOf(20, func() {
		if _, _, _, err := materializeRender(tm, ldr, s, p, "/assets/x.css"); err != nil {
			t.Fatal(err)
		}
	})
	if budget := 10 * time.Millisecond; best > budget {
		t.Errorf("materialization best-of-20 = %v/page, budget %v (§15 class ~1ms)", best, budget)
	}
}

// §15: "Passe CSS (esbuild in-process) — quelques ms".
func TestBudgetCSSPass(t *testing.T) {
	_, _, tm, _ := goldenSite(t)
	best := bestOf(10, func() {
		if _, err := css.BuildTheme(tm); err != nil {
			t.Fatal(err)
		}
	})
	if budget := 20 * time.Millisecond; best > budget {
		t.Errorf("css pass best-of-10 = %v, budget %v (§15 quelques ms)", best, budget)
	}
}
