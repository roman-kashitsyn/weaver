package main

import (
	"container/heap"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var (
	ErrMalformedWeave = errors.New("malformed weave")
)

type Action int

const (
	Add Action = iota
	Del
	Keep
)

type Delta struct {
	Action Action
	Items  []int
}

type InstructionType int

const (
	InstrUnknown InstructionType = iota
	InstrLine
	InstrBeginAdd
	InstrBeginDel
	InstrEnd
)

// Instruction is a weave instruction.
// The three least significant bits indicate the instruction type.
// If it's the "Line" instruction, the other bits indicate the line number.
type Instruction struct {
	Type InstructionType
	// Payload contains the instruction payload.
	// For Line instructions, it's the line number.
	// For other instructions, it's the version ID.
	Payload int
}

func (i Instruction) Line() int {
	return i.Payload
}

func (i Instruction) VersionID() VersionID {
	return VersionID(i.Payload)
}

func (i Instruction) String() string {
	switch i.Type {
	case InstrLine:
		return fmt.Sprintf("%d", i.Payload)
	case InstrBeginAdd:
		return fmt.Sprintf("^AI %d", i.Payload)
	case InstrBeginDel:
		return fmt.Sprintf("^AD %d", i.Payload)
	case InstrEnd:
		return fmt.Sprintf("^AE %d", i.Payload)
	}
	return "unknown"
}

type VersionID int

type Version struct {
	ID          VersionID
	Author      string
	Description string
}

type Weave struct {
	Instructions []Instruction
	Versions     []Version
	Lines        []string
}

type VersionGraph struct{}

// IsAncestorOf defines the causal relationship on the version graph.
// It returns true if one version precedes the other
// (and thus, the later must include all the changes introduced in the former).
func (g VersionGraph) IsAncestorOf(v, w VersionID) bool {
	// TODO: for now, we handle only linear histories.
	return v <= w
}

// ===
// instructionQueue is a priority queue for weave instructions.
type instructionQueue struct {
	vgraph  VersionGraph
	entries []Instruction
}

var _ heap.Interface = &instructionQueue{}

func NewQueue(g VersionGraph) *instructionQueue {
	q := new(instructionQueue)
	q.vgraph = g
	return q
}

func (q *instructionQueue) Push(e any) {
	q.entries = append(q.entries, e.(Instruction))
}

func (q *instructionQueue) Pop() any {
	n := len(q.entries)
	out := q.entries[n-1]
	q.entries = q.entries[:n-1]
	return out
}

func (q *instructionQueue) Swap(i, j int) {
	q.entries[i], q.entries[j] = q.entries[j], q.entries[i]
}

func (q *instructionQueue) Less(i, j int) bool {
	// The most recent version must be at the top.
	return q.vgraph.IsAncestorOf(q.entries[j].VersionID(), q.entries[i].VersionID())
}

func (q *instructionQueue) Len() int {
	return len(q.entries)
}

func (q *instructionQueue) closeInstr(v VersionID) (Instruction, error) {
	i := slices.IndexFunc(q.entries, func(e Instruction) bool { return e.VersionID() == v })
	if i == -1 {
		return Instruction{}, ErrMalformedWeave
	}
	return heap.Remove(q, i).(Instruction), nil
}

// Reconstruct locates the lines that belong to the given version.
// It returns a mask that marks the "Line" instructions enabled for the given version.
func Reconstruct(instructions []Instruction, g VersionGraph, v VersionID) ([]bool, error) {
	mask := make([]bool, len(instructions))
	activeSet := NewQueue(g)
	inactiveSet := NewQueue(g)
	for i, instr := range instructions {
		switch instr.Type {
		case InstrLine:
			if activeSet.Len() == 0 {
				return nil, fmt.Errorf("%w: bare line instruction on line %d", ErrMalformedWeave, i)
			}
			top := activeSet.entries[0]
			if g.IsAncestorOf(top.VersionID(), v) && top.Type == InstrBeginAdd {
				mask[i] = true
			}
		case InstrBeginAdd:
			heap.Push(activeSet, instr)
		case InstrBeginDel:
			if g.IsAncestorOf(instr.VersionID(), v) {
				heap.Push(activeSet, instr)
			} else {
				heap.Push(inactiveSet, instr)
			}
		case InstrEnd:
			_, err := activeSet.closeInstr(instr.VersionID())
			if err != nil {
				if _, err := inactiveSet.closeInstr(instr.VersionID()); err != nil {
					return nil, fmt.Errorf("%w: no matching beginning for instruction %s at %d", err, instr, i)
				}
			}
		}
	}
	return mask, nil
}

type Table struct {
	data []int
	rows int
	cols int
}

func NewTable(n, m int) Table {
	return Table{make([]int, n*m), n, m}
}

func (t Table) At(i, j int) int {
	return t.data[t.cols*i+j]
}

func (t Table) Set(i, j, v int) {
	t.data[t.cols*i+j] = v
}

func (t Table) String() string {
	var buf strings.Builder
	for i := range t.rows {
		for _, v := range t.data[t.cols*i : t.cols*(i+1)] {
			fmt.Fprintf(&buf, "%3d", v)
		}
		buf.WriteRune('\n')
	}
	return buf.String()
}

func FormatScript(script []Delta) string {
	var buf strings.Builder
	for _, delta := range script {
		marker := ' '
		switch delta.Action {
		case Del:
			marker = '-'
		case Add:
			marker = '+'
		}
		for _, item := range delta.Items {
			fmt.Fprintf(&buf, "%c%d\n", marker, item)
		}
	}
	return buf.String()
}

func PrintScript(script []Delta) {
	fmt.Print(FormatScript(script))
}

func DiffScript(a, b []int) []Delta {
	inf := 1<<32 - 1
	n, m := len(a), len(b)
	d := NewTable(n+1, m+1)

	for i := range n + 1 {
		d.Set(i, m, n-i)
	}
	for j := range m + 1 {
		d.Set(n, j, m-j)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			v := inf
			if a[i] == b[j] {
				v = d.At(i+1, j+1)
			}
			v = min(v, 1+min(d.At(i, j+1), d.At(i+1, j)))
			d.Set(i, j, v)
		}
	}

	var script []Delta
	i, j := 0, 0
	for i < n || j < m {
		v := inf
		var delta Delta
		dx, dy := 0, 0
		if i < n && j < m && a[i] == b[j] {
			v = d.At(i+1, j+1)
			delta.Items = []int{a[i]}
			delta.Action = Keep
			dx, dy = 1, 1
		}
		if i < n && d.At(i+1, j) < v {
			v = d.At(i+1, j)
			delta.Action = Del
			delta.Items = []int{a[i]}
			dx, dy = 1, 0
		}
		if j < m && d.At(i, j+1) < v {
			v = d.At(i, j+1)
			delta.Action = Add
			delta.Items = []int{b[j]}
			dx, dy = 0, 1
		}
		k := len(script)
		if k > 0 && script[k-1].Action == delta.Action {
			script[k-1].Items = append(script[k-1].Items, delta.Items[0])
		} else {
			script = append(script, delta)
		}
		i += dx
		j += dy
	}

	return script
}

func main() {
	script := DiffScript([]int{4, 1, 8, 8, 8, 1, 2, 3, 4}, []int{4, 9, 8, 1, 2, 3, 5})
	PrintScript(script)
}
