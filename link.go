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
// linked to), but it is a problem if the original program never imports os or
// bufio, since these are dependencies of the vars package.
//
// To get around this, we use toolexec when building the generated package,
// with an env var containing the path of the linker's importcfg. When this env
// var is set, our only action during 'compile' is to update the current
// package's importcfg so that entries which exist in link's importcfg are used
// whenever possible. This lets us avoid fingerprint mismatches without knowing
// anything about the original build command because the only packages we'll
// use from our build are the ones which were not a part of the build to begin
// with.
func link(args []string) ([]string, error) {
	cfgIdx, cfgPath := getFlag(args, "importcfg")
	cfg, err := readImportCfg(cfgPath)
	if err != nil {
		return args, err
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
	mod := exec.Command("go", "mod", "init", coverPkgPath)
	mod.Dir = coverDir
	if err := mod.Run(); err != nil {
		return args, err
	}
	exe, err := os.Executable()
	if err != nil {
		return args, err
	}

	list := exec.Command("go", "list",
		// Needed for importcfg patching; see fixImportCfg.
		"-toolexec", exe,
		// Doesn't do much, but we want to avoid the tmp dir mattering.
		"-trimpath",
		// Export tells go to actually build the package, and give us export
		// file paths.
		"-export",
		// This just helps debugging.
		"-work",
		// Print the export file and importpath as they'd kind of appear in an
		// importcfg file...
		"-f", "packagefile {{ .ImportPath }}={{ .Export }}",
		// ... for all packages in the build.
		"-deps",
	)
	genCfg := new(importcfg)
	list.Dir = coverDir
	list.Stderr = os.Stderr
	list.Stdout = genCfg
	list.Env = append(list.Environ(),
		// This lets us reference the linker's importcfg when compiling the vars
		// package.
		coverImportcfg+"="+cfgPath,
		// Helpful in debugging; lets us preserve $WORK inside the existing
		// work dir.
		"GOTMPDIR="+filepath.Join(coverDir, "tmp"),
	)
	if err := list.Run(); err != nil {
		return args, err
	}
	delete(genCfg.pkg, "unsafe") // fake

	genExport := genCfg.pkg[coverPkgPath]
	mainExport, err := rebuildMain(filepath.Dir(cfgPath), mainArgs, genExport)
	if err != nil {
		return args, err
	}
	args[len(args)-1] = mainExport

	// replace main in importcfg to point at the new main
	cfg.pkg[cfg.firstPkg] = strings.TrimSpace(string(mainExport))
	// merge importcfg for the generated package into the linker's importcfg,
	// favoring originals; only missing entries are added.
	for pkg, file := range genCfg.pkg {
		if _, ok := cfg.pkg[pkg]; ok {
			continue
		}
		cfg.pkg[pkg] = file
	}
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

func rebuildMain(workDir, args, covervars string) (string, error) {
	argv := strings.Split(args, " ")

	oIdx, o := getFlag(argv, "o")
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
	stderr := bytes.NewBuffer(nil)
	cmd.Dir = workDir
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("rebuild failed (%w): %s", err, stderr)
	}

	return out, nil
}

const writeDotGo = `package covervars

import (
	"bufio"
	"os"
)

func runeFor(i uint8) rune {
	if i == 1 {
		return '1'
	}
	return '0'
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
	w := bufio.NewWriter(f)
	defer w.Flush()
	w.WriteString("mode: set\n")
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

			fmt.Fprintf(init, "\tw.WriteString(%q)\n", block+" 1 ")
			fmt.Fprintf(init, "\tw.WriteRune(runeFor(_cover_%s_%s))\n", suffix, cleanIDPart(id))
			fmt.Fprintf(init, "\tw.WriteRune('\\n')\n")

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

	return readImportCfgFrom(f)
}

func readImportCfgFrom(r io.Reader) (*importcfg, error) {
	cfg := new(importcfg)
	if _, err := io.Copy(cfg, r); err != nil {
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
