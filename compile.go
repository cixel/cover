package main

// TODO add //line directive to tops of files

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func compile(coverPaths []string, tool string, args []string) ([]string, error) {
	importPath := os.Getenv("TOOLEXEC_IMPORTPATH")
	_, buildid := getFlag(args, "buildid")
	_, pkg := getFlag(args, "p")
	_, out := getFlag(args, "o")
	workDir := filepath.Dir(out)
	cfgPathIDx, cfgPath := getFlag(args, "importcfg")
	actionID, _, _ := strings.Cut(buildid, "/")

	if linkPath := os.Getenv(coverImportcfg); linkPath != "" {
		return fixImportCfg(args, linkPath, workDir)
	}

	instrument := slices.Contains(coverPaths, "*") || slices.Contains(coverPaths, importPath)
	instrument = instrument || (len(coverPaths) == 0 && pkg == "main")
	// we need to our "exit hook" to main, even if we're not meant to
	// instrument it for coverage
	if !instrument && pkg != "main" {
		return args, nil
	}

	cacheDir, err := cacheDir()
	if err != nil {
		return args, err
	}

	if err := os.MkdirAll(cacheDir, 0777); err != nil {
		return args, err
	}

	cacheFilePath := filepath.Join(cacheDir, actionID)
	if err := os.MkdirAll(cacheDir, 0777); err != nil {
		return args, err
	}
	cacheFile, err := os.Create(cacheFilePath)
	if err != nil {
		return args, err
	}
	defer cacheFile.Close()
	cache := bufio.NewWriter(cacheFile)

	coverFilePath := filepath.Join(workDir, "_covervars.go")
	// because the linker re-compiles main, we don't want to keep the files we
	// generate for the compiler in $WORK or we risk having them no longer
	// exist on subsequent builds where the main pkg is cached.
	if pkg == "main" {
		coverFilePath = cacheFilePath + "-main"
		if err := os.MkdirAll(coverFilePath, 0777); err != nil {
			return args, err
		}
		if err := copyDir(coverFilePath, workDir); err != nil {
			return args, err
		}
		coverFilePath = filepath.Join(coverFilePath, "_covervars.go")
	}
	coverFile, err := os.Create(coverFilePath)
	if err != nil {
		return args, err
	}
	defer coverFile.Close()
	covervars := bufio.NewWriter(coverFile)
	fmt.Fprintf(covervars, "package %s\n\n", filepath.Base(pkg))
	fmt.Fprint(covervars, "import _ \"unsafe\"\n\n")

	_, files := goFiles(args)
	fset := token.NewFileSet()
	for i, path := range files {
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		parsed, err := parser.ParseFile(fset, path, contents, parser.ParseComments)
		if err != nil {
			return nil, err
		}

		f := &file{
			buf:        newBuffer(contents),
			fset:       fset,
			importPath: importPath,
			syntax:     parsed,
		}

		if instrument {
			ast.Walk(f, parsed)
		}

		if pkg == "main" {
			f.addMainInit()
		}

		for _, b := range f.blocks {
			ce := b.cacheEntry()
			fmt.Fprintln(cache, ce)

			cv := b.coverVar()
			fmt.Fprintf(covervars, "//go:linkname %s %s.%s_%s\n", cv, coverPkgPath, cv, cleanIDPart(actionID))
			fmt.Fprintf(covervars, "func %s() // %s\n\n", cv, ce)
		}

		new := f.buf.bytes()
		outPath := filepath.Join(workDir, "cover."+filepath.Base(path))
		// again, we can't store files meant for the compiler inside $WORK or
		// we risk subsequent (cached) builds failing when we go to recompile
		// during linking.
		if pkg == "main" {
			outPath = filepath.Join(filepath.Dir(coverFilePath), filepath.Base(outPath))
		}
		if err := os.WriteFile(outPath, new, 0666); err != nil {
			return nil, err
		}
		files[i] = outPath
	}

	if pkg == "main" {
		fmt.Fprintf(covervars, "//go:linkname _WriteCoverage %s.WriteCoverage\n", coverPkgPath)
		fmt.Fprint(covervars, "func _WriteCoverage()\n")

		cfgPath = strings.Replace(cfgPath, workDir, filepath.Dir(coverFilePath), 1)
		args[cfgPathIDx] = cfgPath
	}

	args = append(args, coverFilePath)

	cache.WriteString("--\n")
	cache.WriteString(tool)
	for _, arg := range args {
		cache.WriteRune(' ')
		cache.WriteString(arg)
	}
	cache.WriteRune('\n')

	cache.Flush()
	covervars.Flush()

	return args, nil
}

func coverPkgs() []string {
	v := os.Getenv(coverPathsVar)
	if len(v) == 0 {
		return nil
	}
	pkgs := strings.Split(v, ",")
	for i, p := range pkgs {
		pkgs[i] = strings.TrimSpace(p)
	}
	slices.Sort(pkgs)
	return pkgs
}

func fixImportCfg(args []string, linkPath, workDir string) ([]string, error) {
	linkCfg, err := readImportCfg(linkPath)
	if err != nil {
		return args, fmt.Errorf("couldn't read linker importcfg: %w", err)
	}

	idx, cfgPath := getFlag(args, "importcfg")
	cfg, err := readImportCfg(cfgPath)
	if err != nil {
		return args, err
	}

	for pkg := range cfg.pkg {
		orig, ok := linkCfg.pkg[pkg]
		if ok {
			cfg.pkg[pkg] = orig
		}
	}

	newPath := filepath.Join(workDir, "importcfg.cover")
	new, err := os.Create(newPath)
	if err != nil {
		return args, err
	}
	defer new.Close()
	if _, err := cfg.WriteTo(new); err != nil {
		return args, err
	}

	args[idx] = newPath
	return args, nil
}

type block struct {
	file string // importPath:file.go

	start               token.Pos
	startLine, startCol int

	end             token.Pos
	endLine, endCol int
}

func (b block) coverVar() string {
	return fmt.Sprintf(
		"cover_%d_%d",
		b.start, b.end,
	)
}

func (b block) cacheEntry() string {
	return fmt.Sprintf(
		"%s:%d.%d,%d.%d %d_%d",
		b.file, b.startLine, b.startCol, b.endLine, b.endCol,
		b.start, b.end,
	)
}

type file struct {
	buf        *buffer
	fset       *token.FileSet
	importPath string
	syntax     *ast.File

	blocks []block
}

func (f *file) newBlock(pos, end token.Pos) block {
	pPos := f.fset.Position(pos)
	ePos := f.fset.Position(end)
	return block{
		file: fmt.Sprintf("%s/%s", f.importPath, filepath.Base(pPos.Filename)),

		start:     pos,
		startLine: pPos.Line,
		startCol:  pPos.Column,

		end:     end,
		endLine: ePos.Line,
		endCol:  ePos.Column,
	}
}

func (f *file) insert(pos token.Pos, s string) {
	// assume file is the only file in its FileSet, so there's no need to
	// translate Pos to 0-based index
	f.buf.insert(f.fset.Position(pos).Offset, s)
}

func (f *file) Visit(node ast.Node) ast.Visitor {
	if node == nil {
		return nil
	}

	switch n := node.(type) {
	// List of nodes which implement ast.Stmt from:
	// https://github.com/golang/go/blob/09aeb6e33ab426eff4676a3baf694d5a3019e9fc/src/go/ast/ast.go#L849
	case *ast.BadStmt:
	case *ast.DeclStmt:
		f.addStmtCounter(n)
	case *ast.EmptyStmt:
	case *ast.LabeledStmt:
	case *ast.ExprStmt:
		f.addStmtCounter(n)
	case *ast.SendStmt:
		f.addStmtCounter(n)
	case *ast.IncDecStmt:
		f.addStmtCounter(n)
	case *ast.AssignStmt:
		f.addStmtCounter(n)
	case *ast.GoStmt:
	case *ast.DeferStmt:
	case *ast.ReturnStmt:
		f.addStmtCounter(n)
	case *ast.BranchStmt:
	case *ast.BlockStmt:
	case *ast.IfStmt:
		// TODO: do the weird offset else if --> else { if { ... }} thing

		if n.Init != nil {
			f.addCounter(n.Pos(), n.Init.Pos(), n.Init.End())
		}
		ast.Walk(f, n.Body)
		return nil
	case *ast.CaseClause:
		// case/comm clauses aren't covered, only their bodies are.
		for _, s := range n.Body {
			ast.Walk(f, s)
		}
		return nil
	case *ast.CommClause:
		for _, s := range n.Body {
			ast.Walk(f, s)
		}
		return nil
	case *ast.SwitchStmt:
		if n.Init != nil {
			f.addCounter(n.Pos(), n.Init.Pos(), n.Init.End())
		}
		ast.Walk(f, n.Body)
		return nil
	case *ast.TypeSwitchStmt:
		if n.Init != nil {
			f.addCounter(n.Pos(), n.Init.Pos(), n.Init.End())
		}
		ast.Walk(f, n.Body)
		return nil
	case *ast.SelectStmt:
	case *ast.ForStmt:
		// TODO: prepend counter for Init stmt. Post stmt counter should go
		// just in front of the closing Paren (easy) and before scoped
		// "continue" (gross)
		ast.Walk(f, n.Body)
		return nil
	case *ast.RangeStmt:
	}

	return f
}

func (f *file) addCounter(at, start, end token.Pos) {
	b := f.newBlock(start, end)
	str := fmt.Sprintf("%s();", b.coverVar())
	f.insert(at, str)
	f.blocks = append(f.blocks, b)
}

func (f *file) addStmtCounter(stmt ast.Stmt) {
	f.addCounter(stmt.Pos(), stmt.Pos(), stmt.End())
}

func (f *file) addMainInit() {
	var main *ast.FuncDecl
	for _, decl := range f.syntax.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "main" {
			main = d
			break
		}
	}
	if main == nil || main.Body == nil {
		return
	}
	if main.Body == nil {
		return
	}
	f.insert(main.Body.Lbrace+1, `defer _WriteCoverage();`)
}

func copyDir(dst, src string) error {
	files, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0777); err != nil {
		return err
	}
	for _, file := range files {
		srcPath := filepath.Join(src, file.Name())
		dstPath := filepath.Join(dst, file.Name())
		if file.IsDir() {
			if err := copyDir(dstPath, srcPath); err != nil {
				return err
			}
			continue
		}
		if !file.Type().IsRegular() {
			continue
		}
		w, err := os.Create(dstPath)
		if err != nil {
			return err
		}
		defer w.Close()
		r, err := os.Open(srcPath)
		if err != nil {
			return err
		}
		defer r.Close()
		if _, err := io.Copy(w, r); err != nil {
			return err
		}
	}
	return nil
}
