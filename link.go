package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

const (
	coverImportcfg = "COVER_IMPORTCFG"
	coverPkgPath   = "ehden.net/cover/vars"
)

// link generates the ehden.net/cover/vars package with all the cover vars, as
// well as an "init" function that can be called by the defer we add to
// main.main.
//
// We generate the vars by reading the data we've cached for each package in
// the build, and then build it with 'go list -export'.
//
// Because we don't have the build flags go was originally invoked with, we
// can't ensure we build the vars package with all the same flags. This isn't a
// problem for the vars package itself (it's never imported anywhere, only
// linked to), but it is a problem if the original program never imports os,
// since it's a dependency of the vars package.
//
// To avoid fingerprint mismatches, we error out if we can't find os in the
// linker's importcfg. If we can, toolexec swaps the os package in
// ehden.net/cover/var's importcfg file to use the one built earlier.
//
// In the future, it may be an interesting exercise to build os ourselves. This
// is tricky because we'll need to touch up all importcfg files during
// compilation of all of os's dependencies which are not already in the build.
func link(args []string) ([]string, error) {
	cfgIdx, cfgPath := getFlag(args, "importcfg")
	cfg, err := readImportCfg(cfgPath)
	if err != nil {
		return args, err
	}

	if _, ok := cfg.pkg["os"]; !ok {
		return args, errors.New("os package not found in build. add it with 'import _ \"os\"'")
	}

	coverDir := filepath.Join(filepath.Dir(cfgPath), "coverpkg")
	if err := os.MkdirAll(filepath.Join(coverDir, "tmp"), 0777); err != nil {
		return args, err
	}

	mainArgs, err := genCoverVars(cfg, coverDir)
	if err != nil {
		return args, err
	}

	// FIXME: this should probably use the version of Go that link belongs to,
	// not whatever is first in PATH.
	// TODO pull this out
	mod := exec.Command("go", "mod", "init", coverPkgPath)
	mod.Dir = coverDir
	if err := mod.Run(); err != nil {
		return args, err
	}
	exe, err := os.Executable()
	if err != nil {
		return args, err
	}

	list := exec.Command("go", "list", "-toolexec", exe, "-trimpath", "-export", "-f", "{{ .Export }}", "-work")
	list.Dir = coverDir
	list.Stderr = os.Stderr
	list.Env = append(list.Environ(),
		// This lets us reference the linker's importcfg when compiling the vars
		// package.
		coverImportcfg+"="+cfgPath,
		// Helpful in debugging; lets us preserve $WORK inside the existing
		// work dir.
		"GOTMPDIR="+filepath.Join(coverDir, "tmp"),
	)
	varsExport, err := list.Output()
	if err != nil {
		return args, err
	}

	mainExport, err := rebuildMain(mainArgs, string(varsExport))
	if err != nil {
		return args, err
	}
	args[len(args)-1] = mainExport

	cfg.pkg[cfg.firstPkg] = strings.TrimSpace(string(mainExport))
	cfg.pkg[coverPkgPath] = strings.TrimSpace(string(varsExport))
	newImportCfg := filepath.Join(filepath.Dir(cfgPath), "importcfg.cover.link")
	out, err := os.Create(newImportCfg)
	if err != nil {
		return args, err
	}
	defer out.Close()
	if _, err := cfg.WriteTo(out); err != nil {
		return args, err
	}
	args[cfgIdx] = newImportCfg

	return args, nil
}

const mainInitDotGo = `package main

import _ "ehden.net/cover/vars"
`

func rebuildMain(args, covervars string) (string, error) {
	argv := strings.Split(args, " ")

	oIdx, o := getFlag(argv, "o")
	workDir := filepath.Dir(o)
	out := filepath.Join(workDir, "cover"+filepath.Base(o))
	argv[oIdx] = out

	newFile := filepath.Join(workDir, "_cover_init.go")
	if err := os.WriteFile(newFile, []byte(mainInitDotGo), 0666); err != nil {
		return "", err
	}
	argv = append(argv, newFile)

	cfgIdx, cfgPath := getFlag(argv, "importcfg")
	cfg, err := readImportCfg(cfgPath)
	if err != nil {
		return "", err
	}
	cfg.pkg[coverPkgPath] = covervars
	newCfgPath := filepath.Join(workDir, "importcfg.rebuild")
	new, err := os.Create(newCfgPath)
	if err != nil {
		return "", err
	}
	defer new.Close()
	if _, err := cfg.WriteTo(new); err != nil {
		return "", err
	}
	argv[cfgIdx] = newCfgPath

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = workDir
	// cmd.Stderr = os.Stderr // FIXME
	if err := cmd.Run(); err != nil {
		return "", nil
	}

	return out, nil
}

const writeDotGo = `package covervars

import (
	"os"
)

func stringFor(i uint8) string {
	if i == 1 {
		return "1"
	}
	return "0"
}

func WriteCoverage() {
	outPath := "cover.out"
	if p := os.Getenv("COVER_PATH"); p != "" {
		outPath = p
	}
	f, err := os.Create(outPath)
	if err != nil {
		println("ehden.net/fizzbuzz: could not emit coverage data:", err)
	}
	defer f.Close()

	println("[ehden.net/cover] printing coverage profile to", outPath)

	f.WriteString("mode: set\n")
`

// generates the covervars package. returns the compile command used to build
// the main.
func genCoverVars(cfg *importcfg, dir string) (string, error) {
	cacheDir, err := cacheDir()
	if err != nil {
		return "", fmt.Errorf("couldn't read cache dir: %w", err)
	}

	varfile, err := os.Create(filepath.Join(dir, "covervars.go"))
	if err != nil {
		return "", err
	}
	defer varfile.Close()
	initfile, err := os.Create(filepath.Join(dir, "init.go"))
	if err != nil {
		return "", err
	}
	defer initfile.Close()

	init := bufio.NewWriter(initfile)
	init.WriteString(writeDotGo)

	vars := bufio.NewWriter(varfile)
	vars.WriteString("package covervars\n\n")
	vars.WriteString("import _ \"unsafe\"\n\n")

	pkgs := coverPkgs()
	if slices.Contains(pkgs, "*") {
		pkgs = make([]string, 0, len(cfg.pkg))
		for p := range cfg.pkg {
			pkgs = append(pkgs, p)
		}
	} else if len(pkgs) == 0 {
		for pkg := range cfg.pkg {
			if pkg == cfg.firstPkg {
				pkgs = append(pkgs, pkg)
				break
			}
		}
	}

	var main string
	for _, pkg := range pkgs {
		file, ok := cfg.pkg[pkg]
		if !ok {
			continue
		}

		var errs []error
		if err := eachCacheLine(cacheDir, file, func(id, line string, last bool) {
			if last {
				// assume the first packagefile in importcfg.link is the main
				if pkg == cfg.firstPkg {
					main = line
				}
				return
			}

			block, suffix, found := strings.Cut(line, " ")
			if !found {
				errs = append(errs, fmt.Errorf("invalid cache line for %s: %q", pkg, line))
			}

			init.WriteRune('\t')
			fmt.Fprintf(init,
				`f.WriteString(%q + " 1 " + stringFor(_cover_%s_%s) + "\n")`,
				block, suffix, cleanIDPart(id),
			)
			init.WriteRune('\n')

			cv := fmt.Sprintf("cover_%s_%s", suffix, cleanIDPart(id))
			fmt.Fprintf(vars, "var _%s uint8\n", cv)
			fmt.Fprintf(vars, "//go:linkname %s %s.%s\n", cv, coverPkgPath, cv)
			fmt.Fprintf(vars, "func %s() { _%s = 1 } // %s\n\n", cv, cv, block)
		}); err != nil {
			return "", fmt.Errorf("cache read error for %q: %w", pkg, err)
		}
		if len(errs) > 0 {
			return "", fmt.Errorf("cache read error for %q: %w", pkg, errors.Join(errs...))
		}
	}

	init.WriteString("}")
	vars.Flush()
	init.Flush()

	// probably means main was not selected for coverage instrumentation, so we
	// need to search for it in importcfg.link ourselves.
	if main == "" {
		for pkg, file := range cfg.pkg {
			if pkg != cfg.firstPkg {
				continue
			}
			if err := eachCacheLine(cacheDir, file, func(id, line string, last bool) {
				if !last {
					return
				}
				main = line
			}); err != nil {
				return "", fmt.Errorf("cache read error for %q: %w", pkg, err)
			}
		}
	}

	if main == "" {
		return "", fmt.Errorf("couldn't find main package %q in build", cfg.firstPkg)
	}

	return main, nil
}

func readImportCfg(path string) (*importcfg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := new(importcfg)
	if _, err := io.Copy(cfg, f); err != nil {
		return nil, err
	}
	return cfg, nil
}

// holds importcfg data. we want to preserve everything, but only actually care
// about packagefile entries.
type importcfg struct {
	firstPkg string

	// stores packagefile entries
	pkg map[string]string

	// everything else as verbatim lines
	other [][]byte
}

// Write a line from an importcfg file into the importcfg.
func (cfg *importcfg) Write(line []byte) (n int, err error) {
	n = len(line)
	key, val, found := bytes.Cut(line, []byte(" "))
	if !bytes.Equal(key, []byte("packagefile")) || !found {
		cfg.other = append(cfg.other, line)
		return
	}

	pkg, file, found := strings.Cut(string(val), "=")
	if !found {
		return n, fmt.Errorf("invalid packagefile entry: %q", line)
	}

	if cfg.firstPkg == "" {
		cfg.firstPkg = pkg
	}

	if cfg.pkg == nil {
		cfg.pkg = make(map[string]string)
	}
	cfg.pkg[pkg] = file

	return
}

// ReadFrom reads the given io.Reader into the importcfg. Implements
// io.ReaderFrom.
func (cfg *importcfg) ReadFrom(r io.Reader) (int64, error) {
	scanner := bufio.NewScanner(r)

	var n int64
	for scanner.Scan() {
		line := scanner.Bytes()
		n += int64(len(line))

		// ignore comments
		if bytes.HasPrefix(line, []byte("#")) {
			continue
		}

		_, err := cfg.Write(line)
		if err != nil {
			return n, err
		}
	}

	return n, scanner.Err()
}

// WriteTo renders the contents to an importcfg file. Implements
// io.WriterTo.
func (cfg *importcfg) WriteTo(w io.Writer) (int64, error) {
	buf := bufio.NewWriter(w)

	var N int64
	for pkg, file := range cfg.pkg {
		n, err := fmt.Fprintf(buf, "packagefile %s=%s\n", pkg, file)
		N += int64(n)
		if err != nil {
			return N, err
		}
	}
	for _, line := range cfg.other {
		n, err := buf.Write(append(line, '\n'))
		N += int64(n)
		if err != nil {
			return N, err
		}
	}

	err := buf.Flush()
	return N, err
}

func eachCacheLine(cacheDir, export string, fn func(id, line string, last bool)) error {
	id, err := buildid(export)
	if err != nil {
		return err
	}
	id, _, _ = strings.Cut(id, "/")

	f, err := os.Open(filepath.Join(cacheDir, id))
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "--" && scanner.Scan() {
			fn(id, scanner.Text(), true)
			break
		}

		fn(id, line, false)
	}

	return nil
}
