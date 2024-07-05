package main

import (
	"sort"
)

// buffered changes to original slice. a minimal version of cmd/internal/edit.
type buffer struct {
	orig  []byte
	edits edits
}

func newBuffer(b []byte) *buffer {
	return &buffer{orig: b}
}

type edit struct {
	start int
	val   string
}

type edits []edit

func (x edits) Len() int      { return len(x) }
func (x edits) Swap(i, j int) { x[i], x[j] = x[j], x[i] }
func (x edits) Less(i, j int) bool {
	// assume we never have two edits at the same index
	return x[i].start < x[j].start
	// if x[i].start != x[j].start {
	// 	return x[i].start < x[j].start
	// }
	// return x[i].end < x[j].end
}

func (b *buffer) insert(pos int, s string) {
	b.edits = append(b.edits, edit{
		pos, s,
	})
}

func (b *buffer) bytes() []byte {
	sort.Stable(b.edits)

	var (
		out    []byte
		offset int
	)
	for _, e := range b.edits {
		// // TODO: because I'm only inserting, this might not be possible
		// if e.start < offset {
		// 	// e0 := b.edits[i-1]
		// 	// return nil, fmt.Errorf("overlapping edits: ", )
		// 	// panic(fmt.Sprintf("overlapping edits: [%d,%d)->%q, [%d,%d)->%q", e0.start, e0.end, e0.new, e.start, e.end, e.new))
		// }
		out = append(out, b.orig[offset:e.start]...)
		// offset = e.start + len(e.val) - 1
		offset = e.start
		out = append(out, e.val...)
	}
	out = append(out, b.orig[offset:]...)
	return out
}

// switch n := node.(type) {
// case *ast.BlockStmt:
// 	// If it's a switch or select, the body is a list of case clauses; don't tag the block itself.
// 	if len(n.List) > 0 {
// 		switch n.List[0].(type) {
// 		case *ast.CaseClause: // switch
// 			for _, n := range n.List {
// 				clause := n.(*ast.CaseClause)
// 				f.addCounters(clause.Colon+1, clause.Colon+1, clause.End(), clause.Body, false)
// 			}
// 			return f
// 		case *ast.CommClause: // select
// 			for _, n := range n.List {
// 				clause := n.(*ast.CommClause)
// 				f.addCounters(clause.Colon+1, clause.Colon+1, clause.End(), clause.Body, false)
// 			}
// 			return f
// 		}
// 	}
// 	f.addCounters(n.Lbrace, n.Lbrace+1, n.Rbrace+1, n.List, true) // +1 to step past closing brace.
// case *ast.IfStmt:
// 	if n.Init != nil {
// 		ast.Walk(f, n.Init)
// 	}
// 	ast.Walk(f, n.Cond)
// 	ast.Walk(f, n.Body)
// 	if n.Else == nil {
// 		return nil
// 	}
// 	// The elses are special, because if we have
// 	//	if x {
// 	//	} else if y {
// 	//	}
// 	// we want to cover the "if y". To do this, we need a place to drop the counter,
// 	// so we add a hidden block:
// 	//	if x {
// 	//	} else {
// 	//		if y {
// 	//		}
// 	//	}
// 	elseOffset := f.findText(n.Body.End(), "else")
// 	if elseOffset < 0 {
// 		panic("lost else")
// 	}
// 	f.edit.Insert(elseOffset+4, "{")
// 	f.edit.Insert(f.offset(n.Else.End()), "}")
//
// 	// We just created a block, now walk it.
// 	// Adjust the position of the new block to start after
// 	// the "else". That will cause it to follow the "{"
// 	// we inserted above.
// 	pos := f.fset.File(n.Body.End()).Pos(elseOffset + 4)
// 	switch stmt := n.Else.(type) {
// 	case *ast.IfStmt:
// 		block := &ast.BlockStmt{
// 			Lbrace: pos,
// 			List:   []ast.Stmt{stmt},
// 			Rbrace: stmt.End(),
// 		}
// 		n.Else = block
// 	case *ast.BlockStmt:
// 		stmt.Lbrace = pos
// 	default:
// 		panic("unexpected node type in if")
// 	}
// 	ast.Walk(f, n.Else)
// 	return nil
// case *ast.SelectStmt:
// 	// Don't annotate an empty select - creates a syntax error.
// 	if n.Body == nil || len(n.Body.List) == 0 {
// 		return nil
// 	}
// case *ast.SwitchStmt:
// 	// Don't annotate an empty switch - creates a syntax error.
// 	if n.Body == nil || len(n.Body.List) == 0 {
// 		if n.Init != nil {
// 			ast.Walk(f, n.Init)
// 		}
// 		if n.Tag != nil {
// 			ast.Walk(f, n.Tag)
// 		}
// 		return nil
// 	}
// case *ast.TypeSwitchStmt:
// 	// Don't annotate an empty type switch - creates a syntax error.
// 	if n.Body == nil || len(n.Body.List) == 0 {
// 		if n.Init != nil {
// 			ast.Walk(f, n.Init)
// 		}
// 		ast.Walk(f, n.Assign)
// 		return nil
// 	}
// case *ast.FuncDecl:
// 	// Don't annotate functions with blank names - they cannot be executed.
// 	// Similarly for bodyless funcs.
// 	if n.Name.Name == "_" || n.Body == nil {
// 		return nil
// 	}
// 	fname := n.Name.Name
// 	// Skip AddUint32 and StoreUint32 if we're instrumenting
// 	// sync/atomic itself in atomic mode (out of an abundance of
// 	// caution), since as part of the instrumentation process we
// 	// add calls to AddUint32/StoreUint32, and we don't want to
// 	// somehow create an infinite loop.
// 	//
// 	// Note that in the current implementation (Go 1.20) both
// 	// routines are assembly stubs that forward calls to the
// 	// internal/runtime/atomic equivalents, hence the infinite
// 	// loop scenario is purely theoretical (maybe if in some
// 	// future implementation one of these functions might be
// 	// written in Go). See #57445 for more details.
// 	if atomicOnAtomic() && (fname == "AddUint32" || fname == "StoreUint32") {
// 		return nil
// 	}
// 	// Determine proper function or method name.
// 	if r := n.Recv; r != nil && len(r.List) == 1 {
// 		t := r.List[0].Type
// 		star := ""
// 		if p, _ := t.(*ast.StarExpr); p != nil {
// 			t = p.X
// 			star = "*"
// 		}
// 		if p, _ := t.(*ast.Ident); p != nil {
// 			fname = star + p.Name + "." + fname
// 		}
// 	}
// 	walkBody := true
// 	if *pkgcfg != "" {
// 		f.preFunc(n, fname)
// 		if pkgconfig.Granularity == "perfunc" {
// 			walkBody = false
// 		}
// 	}
// 	if walkBody {
// 		ast.Walk(f, n.Body)
// 	}
// 	if *pkgcfg != "" {
// 		flit := false
// 		f.postFunc(n, fname, flit, n.Body)
// 	}
// 	return nil
// case *ast.FuncLit:
// 	// For function literals enclosed in functions, just glom the
// 	// code for the literal in with the enclosing function (for now).
// 	if f.fn.counterVar != "" {
// 		return f
// 	}
//
// 	// Hack: function literals aren't named in the go/ast representation,
// 	// and we don't know what name the compiler will choose. For now,
// 	// just make up a descriptive name.
// 	pos := n.Pos()
// 	p := f.fset.File(pos).Position(pos)
// 	fname := fmt.Sprintf("func.L%d.C%d", p.Line, p.Column)
// 	if *pkgcfg != "" {
// 		f.preFunc(n, fname)
// 	}
// 	if pkgconfig.Granularity != "perfunc" {
// 		ast.Walk(f, n.Body)
// 	}
// 	if *pkgcfg != "" {
// 		flit := true
// 		f.postFunc(n, fname, flit, n.Body)
// 	}
// 	return nil
// }
