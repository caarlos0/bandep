package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// nolint: gochecknoglobals
var (
	version = "dev"
	pkg     = flag.String("pkg", "./...", "package to check")
	bansStr = flag.String("ban", "", "import paths to ban (comma separated list)")
	help    = flag.Bool("help", false, "show context-sensitive help.")
	vers    = flag.Bool("version", false, "show application version.")
)

type bannedError struct {
	Package string
	Imports []string
}

func (e bannedError) Error() string {
	return fmt.Sprintf("%s is using banned dependencies %s", e.Package, strings.Join(e.Imports, ", "))
}

func main() {
	flag.BoolVar(help, "h", false, "Show context-sensitive help.")
	flag.BoolVar(vers, "v", false, "Show application version.")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: bandep [<flags>]

enforce banned dependency imports

Flags:
  -h, --help              Show context-sensitive help.
      --pkg="./..."       Package to check.
      --ban=BAN1,BAN2,... Import paths to ban (comma separated list).
  -v, --version           Show application version.`)
	}
	flag.Parse()

	if *help {
		flag.Usage()
		return
	}
	if *vers {
		fmt.Println(version)
		return
	}

	// Process the ban argument from string to list of strings
	bans := strings.Split(*bansStr, ",")
	for i, ban := range bans {
		bans[i] = strings.TrimSpace(ban)
	}

	if err := check(*pkg, bans); err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		os.Exit(1)
	}
}

func check(path string, bans []string) error {
	if !strings.HasSuffix(path, "/...") {
		return checkPkg(path, bans)
	}
	for _, pkg := range allPackagesInFS(path) {
		if err := checkPkg(pkg, bans); err != nil {
			return err
		}
	}
	return nil
}

func checkPkg(pkg string, bans []string) error {
	packs, err := parser.ParseDir(token.NewFileSet(), pkg, nil, 0)
	if err != nil {
		return fmt.Errorf("failed to parse pkg: %s: %s", pkg, err.Error())
	}
	for _, pack := range packs {
		for _, file := range pack.Files {
			imports := checkBannedImports(file, bans)
			if len(imports) > 0 {
				return bannedError{
					Package: pkg,
					Imports: imports,
				}
			}
		}
	}
	return nil
}

func checkBannedImports(file *ast.File, bans []string) []string {
	var result []string
	for _, imp := range file.Imports {
		var path = imp.Path.Value
		path = strings.Replace(path, `"`, "", -1)
		for _, ban := range bans {
			if ban == path {
				result = append(result, path)
			}
		}
	}
	return result
}

// allPackagesInFS is like allPackages but is passed a pattern
// beginning ./ or ../, meaning it should scan the tree rooted
// at the given directory.  There are ... in the pattern too.
func allPackagesInFS(pattern string) []string {
	pkgs, err := matchPackagesInFS(pattern)
	if len(pkgs) == 0 {
		fmt.Fprintf(os.Stderr, "warning: %q matched no packages\n", pattern)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %q: %v\n", pattern, err)
	}
	return pkgs
}

func matchPackagesInFS(pattern string) ([]string, error) {
	// Find directory to begin the scan.
	// Could be smarter but this one optimization
	// is enough for now, since ... is usually at the
	// end of a path.
	i := strings.Index(pattern, "...")
	dir, _ := path.Split(pattern[:i])

	// pattern begins with ./ or ../.
	// path.Clean will discard the ./ but not the ../.
	// We need to preserve the ./ for pattern matching
	// and in the returned import paths.
	prefix := ""
	if strings.HasPrefix(pattern, "./") {
		prefix = "./"
	}
	match := matchPattern(pattern)

	var pkgs []string
	var err = filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || !fi.IsDir() {
			return nil
		}
		if path == dir {
			// filepath.Walk starts at dir and recurses. For the recursive case,
			// the path is the result of filepath.Join, which calls filepath.Clean.
			// The initial case is not Cleaned, though, so we do this explicitly.
			//
			// This converts a path like "./io/" to "io". Without this step, running
			// "cd $GOROOT/src/pkg; go list ./io/..." would incorrectly skip the io
			// package, because prepending the prefix "./" to the unclean path would
			// result in "././io", and match("././io") returns false.
			path = filepath.Clean(path)
		}

		// Avoid .foo, _foo, and testdata directory trees, but do not avoid "." or "..".
		_, elem := filepath.Split(path)
		dot := strings.HasPrefix(elem, ".") && elem != "." && elem != ".."
		if dot || strings.HasPrefix(elem, "_") || elem == "testdata" || elem == "vendor" {
			return filepath.SkipDir
		}

		name := prefix + filepath.ToSlash(path)
		if !match(name) {
			return nil
		}
		if _, err = build.ImportDir(path, 0); err != nil {
			if _, noGo := err.(*build.NoGoError); !noGo {
				log.Print(err)
			}
			return nil
		}
		pkgs = append(pkgs, name)
		return nil
	})
	return pkgs, err
}

// matchPattern(pattern)(name) reports whether
// name matches pattern.  Pattern is a limited glob
// pattern in which '...' means 'any string' and there
// is no other special syntax.
func matchPattern(pattern string) func(name string) bool {
	re := regexp.QuoteMeta(pattern)
	re = strings.Replace(re, `\.\.\.`, `.*`, -1)
	// Special case: foo/... matches foo too.
	if strings.HasSuffix(re, `/.*`) {
		re = re[:len(re)-len(`/.*`)] + `(/.*)?`
	}
	reg := regexp.MustCompile(`^` + re + `$`)
	return reg.MatchString
}
