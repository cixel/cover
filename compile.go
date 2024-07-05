package main

// TODO configurable packages in env vars raises a small problem for
//	buildID/version - should I bake the env var's value into the buildid?

// TODO add //line directive to tops of files

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

func compile(tool string, args []string) ([]string, error) {
	importPath := os.Getenv("TOOLEXEC_IMPORTPATH")
	_, buildid := getFlag(args, "buildid")
	_, pkg := getFlag(args, "p")
	_, out := getFlag(args, "o")
	workDir := filepath.Dir(out)
	actionID, _, _ := strings.Cut(buildid, "/")

	if importPath == coverPkgPath {
		return fixCoverVarsPkg(args, workDir)
	}

	if importPath != "ehden.net/fizzbuzz" {
		// if importPath != "ehden.net/fizzbuzz" && importPath != "fmt" {
		return args, nil
	}

	cacheDir, err := cacheDir()
	if err != nil {
		return args, nil
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
	coverFile, err := os.Create(coverFilePath)
	if err != nil {
		return args, err
	}
	defer coverFile.Close()
	covervars := bufio.NewWriter(coverFile)
	fmt.Fprintf(covervars, "package %s\n\n", pkg)
	covervars.WriteString("import _ \"unsafe\"\n\n")

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
			pkg:        pkg,
			importPath: importPath,
			syntax:     parsed,
		}
		ast.Walk(f, parsed)

		// TODO there may be a better way to get this so that i don't need to
		// have both f.pkg and f.importpath
		if f.pkg == "main" {
			f.addMainInit()
		}

		for _, b := range f.blocks {
			cv := b.coverVar()
			ce := b.cacheEntry()

			cache.WriteString(ce)
			cache.WriteRune('\n')

			fmt.Fprintf(covervars, "//go:linkname %s %s.%s_%s\n", cv, coverPkgPath, cv, cleanIDPart(actionID))
			fmt.Fprintf(covervars, "func %s() // %s\n\n", cv, ce)
		}

		new := f.buf.bytes()
		outPath := filepath.Join(workDir, "cover."+filepath.Base(path))
		if err := os.WriteFile(outPath, new, 0666); err != nil {
			return nil, err
		}
		files[i] = outPath
	}

	// TODO: i might need to generate the package here and have it imported by
	// main to get around the relocation target errors i'm seeing. not sure how
	// I'll know about all the packages in the build here though, since I won't
	// have the linker's importcfg.
	//
	// XXX: maybe have the linker recompile main? main saves its args in a file
	// in the cacheDir and link recompiles it w/ updated importcfg
	if pkg == "main" {
		// // TODO replace occurrence of the generated pkg in strings w/ const
		fmt.Fprintf(covervars, "//go:linkname _WriteCoverage %s.WriteCoverage\n", coverPkgPath)
		fmt.Fprint(covervars, "func _WriteCoverage()\n")
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

func fixCoverVarsPkg(args []string, workDir string) ([]string, error) {
	linkPath := os.Getenv(coverImportcfg)
	if linkPath == "" {
		return args, errors.New("couldn't find linker importcfg")
	}
	linkCfg, err := readImportCfg(linkPath)
	if err != nil {
		return args, fmt.Errorf("couldn't read linker importcfg: %w", err)
	}

	idx, cfgPath := getFlag(args, "importcfg")
	cfg, err := readImportCfg(cfgPath)
	if err != nil {
		return args, err
	}

	cfg.pkg["os"] = linkCfg.pkg["os"]
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
	buf             *buffer
	fset            *token.FileSet
	pkg, importPath string
	syntax          *ast.File

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
