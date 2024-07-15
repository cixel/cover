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
		out = append(out, b.orig[offset:e.start]...)
		out = append(out, e.val...)
		offset = e.start
	}
	out = append(out, b.orig[offset:]...)
	return out
}
