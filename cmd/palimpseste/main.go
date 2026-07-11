// Command palimpseste is the single binary's three temporal modes (§2.1):
// `build` materializes a site directory into a deterministic tree of static
// files, `edit` is the ephemeral localhost editor for in-place authoring, and
// `serve` is the optional convenience server for public/ — any static host
// does the same job.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"

	"palimpseste/internal/build"
	"palimpseste/internal/editserver"
	"palimpseste/internal/lint"
)

// version is overridable at link time: -ldflags "-X main.version=1.2.3".
var version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(cmdBuild(os.Args[2:]))
	case "edit":
		os.Exit(cmdEdit(os.Args[2:]))
	case "serve":
		os.Exit(cmdServe(os.Args[2:]))
	case "theme":
		os.Exit(cmdTheme(os.Args[2:]))
	case "passwd":
		os.Exit(cmdPasswd(os.Args[2:]))
	case "publish":
		os.Exit(cmdPublish(os.Args[2:]))
	case "version", "--version", "-version":
		fmt.Printf("palimpseste %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "palimpseste: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `palimpseste — deterministic static-site compiler

usage:
  palimpseste build [flags]        materialize a site into static files
  palimpseste edit [flags]         edit content in place via a local editor
  palimpseste serve [flags]        convenience static server for public/
  palimpseste theme list  [flags]           list available themes
  palimpseste theme check [flags] <name>    compatibility report (§5.3)
  palimpseste theme apply [flags] <name>    check, migrate, switch, commit
  palimpseste passwd                        hash an admin password for edit --listen
  palimpseste publish [flags]               deploy per site.json (git-push) (§13)
  palimpseste version              print version
  palimpseste help                 show this help

build flags:
  -site dir    site directory holding site.json, content/, themes/ (default ".")
  -out dir     explicit output directory, replaced atomically; when omitted the
               build publishes into <site>/builds/<hash>/ and swaps the
               <site>/public symlink onto it (rollback = re-point the link)
  -check       run content checks and report issues
  -strict      with -check, exit non-zero if any issue is found
  -no-cache    disable incremental build memoisation

edit flags:
  -site dir    site directory holding site.json, content/, themes/ (default ".")
  -addr host   address to bind (default "127.0.0.1:7777")
  -open        open the editor in your browser
  -listen      remote mode: authenticate (PALIMPSESTE_ADMIN_HASH) and allow a
               routable bind address (§8, §14)

serve flags:
  -site dir    site directory whose public/ to serve (default ".")
  -dir dir     serve this directory instead of <site>/public
  -addr host   address to bind (default "127.0.0.1:8000")
`)
}

func cmdBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	siteDir := fs.String("site", ".", "site directory (site.json, content/, themes/)")
	outDir := fs.String("out", "", "explicit output directory (default: publish to <site>/public via builds/<hash>)")
	check := fs.Bool("check", false, "run content checks and report issues")
	strict := fs.Bool("strict", false, "with -check, exit non-zero on any issue")
	noCache := fs.Bool("no-cache", false, "disable incremental build memoisation")
	_ = fs.Parse(args)

	// Memoise renders in <site>/.palimpseste/cache so a rebuild only touches
	// pages whose inputs changed. The cache lives beside the source (like .git),
	// outside content/ and themes/, so it never feeds the content hash.
	cacheDir := ""
	if !*noCache {
		cacheDir = filepath.Join(*siteDir, ".palimpseste", "cache")
	}

	res, err := build.Run(build.Options{
		SiteDir:  *siteDir,
		OutDir:   *outDir,
		Check:    *check,
		Version:  version,
		CacheDir: cacheDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}

	if res.Cached > 0 {
		fmt.Printf("built %d page(s) (%d reused from cache), %d asset(s) -> %s\n", res.Pages, res.Cached, len(res.Assets), res.OutDir)
	} else {
		fmt.Printf("built %d page(s), %d asset(s) -> %s\n", res.Pages, len(res.Assets), res.OutDir)
	}

	errCount := 0
	for _, is := range res.Issues {
		fmt.Fprintln(os.Stderr, is.String())
		if is.Severity == lint.Error {
			errCount++
		}
	}
	if errCount > 0 {
		fmt.Fprintf(os.Stderr, "palimpseste: %d error issue(s)\n", errCount)
		return 1
	}
	if *strict && len(res.Issues) > 0 {
		fmt.Fprintf(os.Stderr, "palimpseste: %d issue(s) under -strict\n", len(res.Issues))
		return 1
	}
	return 0
}

func cmdEdit(args []string) int {
	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	siteDir := fs.String("site", ".", "site directory (site.json, content/, themes/)")
	addr := fs.String("addr", editserver.DefaultAddr, "address to bind")
	open := fs.Bool("open", false, "open the editor in your browser")
	listen := fs.Bool("listen", false, "remote mode: authenticate and allow a routable bind address (§8)")
	_ = fs.Parse(args)

	// Remote mode reads the admin password hash from the environment, never the
	// repository (§3.1, §14). Generate one with `palimpseste passwd`.
	passwordHash := ""
	if *listen {
		passwordHash = os.Getenv("PALIMPSESTE_ADMIN_HASH")
		if passwordHash == "" {
			fmt.Fprintln(os.Stderr, "palimpseste: edit --listen exige PALIMPSESTE_ADMIN_HASH (voir `palimpseste passwd`)")
			return 1
		}
		if *addr == editserver.DefaultAddr {
			*addr = "0.0.0.0:7777" // a routable default once authenticated
		}
	}

	srv, err := editserver.New(editserver.Options{
		SiteDir:      *siteDir,
		Addr:         *addr,
		Version:      version,
		PasswordHash: passwordHash,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}

	// Bind here so a port clash is reported crisply and the printed URL reflects
	// the address actually listening.
	ln, err := net.Listen("tcp", srv.Addr())
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: écoute sur %s: %v\n", srv.Addr(), err)
		return 1
	}
	editURL := "http://" + ln.Addr().String()
	fmt.Printf("palimpseste edit — %s\n", editURL)
	fmt.Println("Modifiez la page directement ; Ctrl+S enregistre, Ctrl+C quitte.")

	if *open {
		openBrowser(editURL)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := srv.Serve(ctx, ln); err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	fmt.Println("\npalimpseste edit — arrêté.")
	return 0
}

// cmdServe is the §2.1 convenience mode: a plain static file server over the
// published tree. Deliberately unremarkable — any static host replaces it —
// but honest about what it serves: the public/ symlink is followed, pretty
// URLs resolve via index.html, and responses carry nosniff like the editor's.
func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	siteDir := fs.String("site", ".", "site directory whose public/ to serve")
	dir := fs.String("dir", "", "serve this directory instead of <site>/public")
	addr := fs.String("addr", "127.0.0.1:8000", "address to bind")
	_ = fs.Parse(args)

	root := *dir
	if root == "" {
		root = filepath.Join(*siteDir, "public")
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "palimpseste: %q n'est pas un répertoire servable (lancer `palimpseste build` d'abord ?)\n", root)
		return 1
	}

	files := http.FileServer(http.Dir(root))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palimpseste: écoute sur %s: %v\n", *addr, err)
		return 1
	}
	fmt.Printf("palimpseste serve — http://%s (%s)\n", ln.Addr().String(), root)
	fmt.Println("Ctrl+C quitte.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	srv := &http.Server{Handler: handler}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "palimpseste: %v\n", err)
		return 1
	}
	fmt.Println("\npalimpseste serve — arrêté.")
	return 0
}

// openBrowser makes a best-effort attempt to open url in the desktop browser. A
// missing opener is not an error: the URL is already printed for the operator.
func openBrowser(url string) {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	_ = exec.Command(name, args...).Start()
}
