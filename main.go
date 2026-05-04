package main

import (
	"container/heap"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

var (
	ErrBadWeave = errors.New("E001: malformed weave")
	ErrBadDelta = errors.New("E002: bad delta")
)

type Action int

const (
	Insert Action = iota
	Delete
	Keep
)

type Delta struct {
	Action Action
	Items  []int
}

func EqualDeltas(x, y Delta) bool {
	return x.Action == y.Action && slices.Equal(x.Items, y.Items)
}

type InstructionType int

const (
	InstrUnknown InstructionType = iota
	InstrLine
	InstrBeginInsert
	InstrBeginDelete
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
	case InstrBeginInsert:
		return fmt.Sprintf("^AI %d", i.Payload)
	case InstrBeginDelete:
		return fmt.Sprintf("^AD %d", i.Payload)
	case InstrEnd:
		return fmt.Sprintf("^AE %d", i.Payload)
	}
	return "unknown"
}

func FormatWeave(weave []Instruction) string {
	var buf strings.Builder
	for _, instr := range weave {
		buf.WriteString(instr.String())
		buf.WriteRune('\n')
	}
	return buf.String()
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

// === Version graph

type VersionGraph struct{}

// IsAncestorOf defines the causal relationship on the version graph.
// It returns true if one version precedes the other
// (and thus, the later must include all the changes introduced in the former).
func (g VersionGraph) IsAncestorOf(v, w VersionID) bool {
	// TODO: for now, we handle only linear histories.
	return v <= w
}

// === String pool

type StringPool struct {
	lines       []string
	lineToIndex map[string]int
}

func NewStringPool(lines []string) *StringPool {
	lineToIndex := make(map[string]int, 2*len(lines))
	for i, line := range lines {
		_, found := lineToIndex[line]
		if !found {
			lineToIndex[line] = i
		}
	}
	return &StringPool{
		lines:       lines,
		lineToIndex: lineToIndex,
	}
}

func (s *StringPool) Intern(line string) int {
	idx, found := s.lineToIndex[line]
	if found {
		return idx
	}
	idx = len(s.lines)
	s.lineToIndex[line] = idx
	s.lines = append(s.lines, line)
	return idx
}

func (s *StringPool) Lines() []string {
	return s.lines
}

// === Active set

type ActiveSet []bool

func (s ActiveSet) Contains(v VersionID) bool {
	if v < 0 {
		return false
	}
	i := int(v)
	return i < len(s) && s[i]
}

// === Priority queue for deltas.

// instructionQueue is a priority queue for weave instructions.
type instructionQueue struct {
	entries []Instruction
}

var _ heap.Interface = &instructionQueue{}

func NewQueue() *instructionQueue {
	return new(instructionQueue)
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
	return q.entries[j].VersionID() < q.entries[i].VersionID()
}

func (q *instructionQueue) Len() int {
	return len(q.entries)
}

func (q *instructionQueue) closeInstr(v VersionID) (Instruction, error) {
	i := slices.IndexFunc(q.entries, func(e Instruction) bool { return e.VersionID() == v })
	if i == -1 {
		return Instruction{}, ErrBadWeave
	}
	return heap.Remove(q, i).(Instruction), nil
}

type weaveScanner struct {
	activeSet ActiveSet
	active    *instructionQueue
	open      map[VersionID]openSpan
}

type openSpan struct {
	instr         Instruction
	inActiveQueue bool
	line          int
}

func newWeaveScanner(activeSet ActiveSet) *weaveScanner {
	return &weaveScanner{
		activeSet: activeSet,
		active:    NewQueue(),
		open:      make(map[VersionID]openSpan),
	}
}

func (s *weaveScanner) begin(instr Instruction, line int) error {
	return s.beginSpan(instr, line, false)
}

func (s *weaveScanner) beginIgnored(instr Instruction, line int) error {
	return s.beginSpan(instr, line, true)
}

func (s *weaveScanner) beginSpan(instr Instruction, line int, ignored bool) error {
	v := instr.VersionID()
	if previous, ok := s.open[v]; ok {
		return fmt.Errorf("%w: duplicate beginning for version %d at line %d; previous beginning was at line %d",
			ErrBadWeave, v, line, previous.line)
	}

	active := false
	switch instr.Type {
	case InstrBeginInsert:
		active = !ignored
	case InstrBeginDelete:
		active = !ignored && s.activeSet.Contains(v)
	default:
		return fmt.Errorf("%w: unexpected beginning instruction %s at line %d", ErrBadWeave, instr, line)
	}
	if active {
		heap.Push(s.active, instr)
	}
	s.open[v] = openSpan{
		instr:         instr,
		inActiveQueue: active,
		line:          line,
	}
	return nil
}

func (s *weaveScanner) close(v VersionID, line int) error {
	span, ok := s.open[v]
	if !ok {
		return fmt.Errorf("%w: no matching beginning for version %d at line %d", ErrBadWeave, v, line)
	}
	if span.inActiveQueue {
		if _, err := s.active.closeInstr(v); err != nil {
			return fmt.Errorf("%w: active span for version %d opened at line %d was missing from active queue at line %d",
				err, v, span.line, line)
		}
	}
	delete(s.open, v)
	return nil
}

func (s *weaveScanner) lineVersion(line int) (VersionID, bool, error) {
	if s.active.Len() == 0 {
		return 0, false, fmt.Errorf("%w: bare line instruction on line %d", ErrBadWeave, line)
	}
	top := s.active.entries[0]
	if s.activeSet.Contains(top.VersionID()) && top.Type == InstrBeginInsert {
		return top.VersionID(), true, nil
	}
	return 0, false, nil
}

func (s *weaveScanner) finish() error {
	if len(s.open) == 0 {
		return nil
	}
	var span openSpan
	first := true
	for _, current := range s.open {
		if first || current.line < span.line {
			span = current
			first = false
		}
	}
	return fmt.Errorf("%w: unclosed instruction %s opened at line %d", ErrBadWeave, span.instr, span.line)
}

// Reconstruct locates the lines that belong to the given version.
// It returns a mask that marks the "Line" instructions enabled for the given version
// and the IDs of versions to which the enabled lines are attributed.
//
// Returns the [ErrBadWeave] if the instructions aren't a valid weave.
func Reconstruct(instructions []Instruction, activeSet ActiveSet) ([]bool, []VersionID, error) {
	mask := make([]bool, len(instructions))
	var versions []VersionID
	scanner := newWeaveScanner(activeSet)
	for i, instr := range instructions {
		switch instr.Type {
		case InstrLine:
			version, ok, err := scanner.lineVersion(i)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				mask[i] = true
				versions = append(versions, version)
			}
		case InstrBeginInsert, InstrBeginDelete:
			if err := scanner.begin(instr, i); err != nil {
				return nil, nil, err
			}
		case InstrEnd:
			if err := scanner.close(instr.VersionID(), i); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("%w: unknown instruction %s at %d", ErrBadWeave, instr, i)
		}
	}
	if err := scanner.finish(); err != nil {
		return nil, nil, err
	}
	return mask, versions, nil
}

// Interleave extends a weave with a new delta.
//
// Returns [ErrBadWeave] if the instructions aren't a valid weave.
// Returns [ErrBadDelta] if the deltas don't apply to the weave cleanly.
func Interleave(instructions []Instruction, deltas []Delta, activeSet ActiveSet, v VersionID) ([]Instruction, error) {
	mask, _, err := Reconstruct(instructions, activeSet)
	if err != nil {
		return nil, err
	}
	var out []Instruction
	i, j, n, m := 0, 0, len(instructions), len(deltas)
	for i < n || j < m {
		// Advance to the next activated line.
		for i < n && !mask[i] {
			out = append(out, instructions[i])
			i++
		}
		if j < m {
			delta := deltas[j]
			switch delta.Action {
			case Insert:
				out = append(out, Instruction{InstrBeginInsert, int(v)})
				for _, item := range delta.Items {
					out = append(out, Instruction{InstrLine, item})
				}
				out = append(out, Instruction{InstrEnd, int(v)})
			case Delete:
				out = append(out, Instruction{InstrBeginDelete, int(v)})
				skipped := 0
				for skipped < len(delta.Items) {
					if i >= n {
						return nil, fmt.Errorf("%w: bad deletion at %d", ErrBadDelta, j)
					}
					out = append(out, instructions[i])
					if mask[i] {
						if delta.Items[skipped] != instructions[i].Line() {
							return nil, fmt.Errorf("%w: mismatched line %d in hunk %d", ErrBadDelta, skipped+1, j+1)
						}
						skipped++
					}
					i++
				}
				out = append(out, Instruction{InstrEnd, int(v)})
			case Keep:
				skipped := 0
				for skipped < len(delta.Items) {
					if i >= n {
						return nil, ErrBadDelta
					}
					out = append(out, instructions[i])
					if mask[i] {
						if delta.Items[skipped] != instructions[i].Line() {
							return nil, fmt.Errorf("%w: mismatched line %d in hunk %d", ErrBadDelta, skipped+1, j+1)
						}
						skipped++
					}
					i++
				}
			}
			j++
		} else if i < n {
			return nil, fmt.Errorf("%w: delta does not cover all lines in base", ErrBadDelta)
		}

	}
	return out, nil
}

// ReconstructDelta extracts from a weave the changes introduced by the specified version.
func ReconstructDelta(instructions []Instruction, v VersionID, activeSet ActiveSet) ([]Delta, error) {
	var deltas []Delta
	delta := Delta{Action: Keep, Items: nil}
	scanner := newWeaveScanner(activeSet)

	for i, instr := range instructions {
		switch instr.Type {
		case InstrLine:
			if delta.Action == Insert {
				delta.Items = append(delta.Items, instr.Line())
			} else {
				_, ok, err := scanner.lineVersion(i)
				if err != nil {
					return nil, err
				}
				if ok {
					delta.Items = append(delta.Items, instr.Line())
				}
			}
		case InstrBeginInsert:
			if instr.VersionID() == v {
				if err := scanner.beginIgnored(instr, i); err != nil {
					return nil, err
				}
				if len(delta.Items) > 0 {
					deltas = append(deltas, delta)
				}
				delta.Action = Insert
				delta.Items = nil
			} else {
				if err := scanner.begin(instr, i); err != nil {
					return nil, err
				}
			}
		case InstrBeginDelete:
			if instr.VersionID() == v {
				if err := scanner.beginIgnored(instr, i); err != nil {
					return nil, err
				}
				if len(delta.Items) > 0 {
					deltas = append(deltas, delta)
				}
				delta.Action = Delete
				delta.Items = nil
			} else {
				if err := scanner.begin(instr, i); err != nil {
					return nil, err
				}
			}
		case InstrEnd:
			if err := scanner.close(instr.VersionID(), i); err != nil {
				return nil, err
			}
			if instr.VersionID() == v {
				if len(delta.Items) > 0 {
					deltas = append(deltas, delta)
				}
				delta.Action = Keep
				delta.Items = nil
			}
		default:
			return nil, fmt.Errorf("%w: unknown instruction %s at %d", ErrBadWeave, instr, i)
		}
	}
	if err := scanner.finish(); err != nil {
		return nil, err
	}
	if len(delta.Items) > 0 {
		deltas = append(deltas, delta)
	}
	return deltas, nil
}

func FormatScript(script []Delta) string {
	var buf strings.Builder
	for _, delta := range script {
		marker := ' '
		switch delta.Action {
		case Delete:
			marker = '-'
		case Insert:
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
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
	}

	for i := range n + 1 {
		d[i][m] = n - i
	}
	for j := range m + 1 {
		d[n][j] = m - j
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			v := inf
			if a[i] == b[j] {
				v = d[i+1][j+1]
			}
			v = min(v, 1+min(d[i][j+1], d[i+1][j]))
			d[i][j] = v
		}
	}

	var script []Delta
	i, j := 0, 0
	for i < n || j < m {
		v := inf
		var delta Delta
		dx, dy := 0, 0
		if i < n && j < m && a[i] == b[j] {
			v = d[i+1][j+1]
			delta.Items = []int{a[i]}
			delta.Action = Keep
			dx, dy = 1, 1
		}
		if i < n && d[i+1][j] < v {
			v = d[i+1][j]
			delta.Action = Delete
			delta.Items = []int{a[i]}
			dx, dy = 1, 0
		}
		if j < m && d[i][j+1] < v {
			v = d[i][j+1]
			delta.Action = Insert
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

func FormatUnifiedDiff(w io.Writer, filename string, deltas []Delta, lines []string, context int) error {
	if _, err := fmt.Fprintf(w, "--- %s\n+++ %s\n", filename, filename); err != nil {
		return err
	}

	writeLines := func(marker byte, items []int) error {
		for _, l := range items {
			if _, err := fmt.Fprintf(w, "%c%s\n", marker, lines[l]); err != nil {
				return err
			}
		}
		return nil
	}

	lineNumber := 1
	baseLineNumber := 1
	n := len(deltas)
	for hunkBegin := 0; hunkBegin < n; {
		// Skip unchanged lines between hunks.
		for hunkBegin < n && deltas[hunkBegin].Action == Keep {
			lineNumber += len(deltas[hunkBegin].Items)
			baseLineNumber += len(deltas[hunkBegin].Items)
			hunkBegin++
		}
		if hunkBegin >= n {
			break
		}

		// Find the extent of this hunk: include all changes and any Keep
		// blocks that are small enough to be absorbed as intra-hunk context.
		hunkEnd := hunkBegin
		hunkBaseLineCount := 0
		hunkNewLineCount := 0
		for hunkEnd < n && (deltas[hunkEnd].Action != Keep || len(deltas[hunkEnd].Items) <= 2*context) {
			delta := deltas[hunkEnd]
			if delta.Action != Insert {
				hunkBaseLineCount += len(delta.Items)
			}
			if delta.Action != Delete {
				hunkNewLineCount += len(delta.Items)
			}
			hunkEnd++
		}

		// Compute pre/post context from adjacent Keep blocks.
		var preContext, postContext []int
		if hunkBegin > 0 {
			items := deltas[hunkBegin-1].Items
			preContext = items[len(items)-min(len(items), context):]
		}
		if hunkEnd < n {
			items := deltas[hunkEnd].Items
			postContext = items[:min(len(items), context)]
		}

		hunkBaseStart := baseLineNumber - len(preContext)
		hunkNewStart := lineNumber - len(preContext)
		hunkBaseLen := hunkBaseLineCount + len(preContext) + len(postContext)
		hunkNewLen := hunkNewLineCount + len(preContext) + len(postContext)
		if _, err := fmt.Fprintf(w, "@@ -%d,%d +%d,%d @@\n", hunkBaseStart, hunkBaseLen, hunkNewStart, hunkNewLen); err != nil {
			return err
		}

		if err := writeLines(' ', preContext); err != nil {
			return err
		}
		for hunkBegin < hunkEnd {
			delta := deltas[hunkBegin]
			var marker byte
			switch delta.Action {
			case Delete:
				marker = '-'
				baseLineNumber += len(delta.Items)
			case Insert:
				marker = '+'
				lineNumber += len(delta.Items)
			case Keep:
				marker = ' '
				lineNumber += len(delta.Items)
				baseLineNumber += len(delta.Items)
			}
			if err := writeLines(marker, delta.Items); err != nil {
				return err
			}
			hunkBegin++
		}
		if err := writeLines(' ', postContext); err != nil {
			return err
		}
	}
	return nil
}

// Next steps:
// - [ ] Add support for version graphs.
// - [ ] Implement serialization/deserialization for weave files.
// - [ ] Implement basic commands.
// - [ ] Implement automatic merge (hard).

func main() {
	script := DiffScript([]int{4, 1, 8, 8, 8, 1, 2, 3, 4}, []int{4, 9, 8, 1, 2, 3, 5})
	PrintScript(script)
}
