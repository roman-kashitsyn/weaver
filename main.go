package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	osuser "os/user"
	"path/filepath"
	"slices"
	"strconv"
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
	Parents     []VersionID
	Author      string
	Description string
}

type Weave struct {
	Head         VersionID
	Instructions []Instruction
	Versions     []Version
	Lines        []string
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

// Contains returns true if version v is in active set.
func (s ActiveSet) Contains(v VersionID) bool {
	return v >= 0 && int(v) < len(s) && s[v]
}

// Activate returns a new active set that also contains v.
func (s ActiveSet) Activate(v VersionID) ActiveSet {
	out := slices.Clone(s)
	out[v] = true
	return out
}

// Deactivate returns a new active set that doesn't contain v.
func (s ActiveSet) Disactivate(v VersionID) ActiveSet {
	out := slices.Clone(s)
	out[v] = false
	return out
}

// Reconstruct locates the lines enabled in the given version set.
// It returns the boolean mask of enabled "Line" instructions
// and the IDs of versions that introduced the enabled lines.
//
// Returns the [ErrBadWeave] if the instructions aren't a valid weave.
func Reconstruct(
	instructions []Instruction,
	activeSet ActiveSet,
) (mask []bool, versions []VersionID, err error) {
	// activeQueue is the priority queue of active open blocks.
	var activeQueue []Instruction
	topActive := func() Instruction {
		return slices.MaxFunc(activeQueue, func(l, r Instruction) int {
			return cmp.Compare(l.VersionID(), r.VersionID())
		})
	}
	openBlocksByVersion := make(map[VersionID]int)
	mask = make([]bool, len(instructions))

	for offset, instr := range instructions {
		switch instr.Type {
		case InstrLine:
			if len(activeQueue) == 0 {
				return nil, nil, fmt.Errorf("%w: bare line instruction at offset %d", ErrBadWeave, offset)
			}
			top := topActive()
			if activeSet.Contains(top.VersionID()) && top.Type == InstrBeginInsert {
				mask[offset] = true
				versions = append(versions, top.VersionID())
			}
		case InstrBeginInsert, InstrBeginDelete:
			v := instr.VersionID()
			if previous, ok := openBlocksByVersion[v]; ok {
				return nil, nil, fmt.Errorf("%w: nested block for version %d at offset %d; previous beginning was at offset %d",
					ErrBadWeave, v, offset, previous)
			}
			if instr.Type == InstrBeginInsert || activeSet.Contains(v) {
				activeQueue = append(activeQueue, instr)
			}
			openBlocksByVersion[v] = offset
		case InstrEnd:
			v := instr.VersionID()
			if _, ok := openBlocksByVersion[v]; !ok {
				return nil, nil, fmt.Errorf("%w: no matching beginning for version %d at offset %d", ErrBadWeave, v, offset)
			}
			delIndex := slices.IndexFunc(activeQueue, func(instr Instruction) bool {
				return instr.VersionID() == v
			})
			if delIndex >= 0 {
				activeQueue = slices.Delete(activeQueue, delIndex, delIndex+1)
			}
			delete(openBlocksByVersion, v)
		default:
			return nil, nil, fmt.Errorf("%w: unknown instruction %s at offset %d", ErrBadWeave, instr, offset)
		}
	}
	if len(openBlocksByVersion) > 0 {
		firstOpen := slices.Min(slices.Collect(maps.Values(openBlocksByVersion)))
		return nil, nil, fmt.Errorf("%w: unclosed instruction %s opened at offset %d", ErrBadWeave, instructions[firstOpen], firstOpen)
	}
	return mask, versions, nil
}

// Interleave extends a weave with a new delta.
//
// Returns [ErrBadWeave] if the instructions aren't a valid weave.
// Returns [ErrBadDelta] if the deltas don't apply to the weave cleanly.
func Interleave(
	instructions []Instruction,
	deltas []Delta,
	activeSet ActiveSet,
	v VersionID,
) (result []Instruction, err error) {
	mask, _, err := Reconstruct(instructions, activeSet)
	if err != nil {
		return nil, err
	}
	i, j, n, m := 0, 0, len(instructions), len(deltas)
	for i < n || j < m {
		// Advance to the next activated line.
		for i < n && !mask[i] {
			result = append(result, instructions[i])
			i++
		}
		if j < m {
			delta := deltas[j]
			switch delta.Action {
			case Insert:
				result = append(result, Instruction{InstrBeginInsert, int(v)})
				for _, item := range delta.Items {
					result = append(result, Instruction{InstrLine, item})
				}
				result = append(result, Instruction{InstrEnd, int(v)})
			case Delete:
				result = append(result, Instruction{InstrBeginDelete, int(v)})
				skipped := 0
				for skipped < len(delta.Items) {
					if i >= n {
						return nil, fmt.Errorf("%w: bad deletion at %d", ErrBadDelta, j)
					}
					result = append(result, instructions[i])
					if mask[i] {
						if delta.Items[skipped] != instructions[i].Line() {
							return nil, fmt.Errorf("%w: mismatched line %d in hunk %d", ErrBadDelta, skipped+1, j+1)
						}
						skipped++
					}
					i++
				}
				result = append(result, Instruction{InstrEnd, int(v)})
			case Keep:
				skipped := 0
				for skipped < len(delta.Items) {
					if i >= n {
						return nil, ErrBadDelta
					}
					result = append(result, instructions[i])
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
	return result, nil
}

// ReconstructDelta extracts from a weave the changes introduced by the specified version.
func ReconstructDelta(
	instructions []Instruction,
	v VersionID,
	activeSet ActiveSet,
) (deltas []Delta, err error) {
	before, _, err := Reconstruct(instructions, activeSet.Disactivate(v))
	if err != nil {
		return nil, err
	}
	after, _, err := Reconstruct(instructions, activeSet.Activate(v))
	if err != nil {
		return nil, err
	}

	var currentDelta Delta
	emit := func(action Action, line int) {
		if currentDelta.Action != action && len(currentDelta.Items) > 0 {
			deltas = append(deltas, currentDelta)
			currentDelta.Items = nil
		}
		currentDelta.Action = action
		currentDelta.Items = append(currentDelta.Items, line)
	}

	for i, instr := range instructions {
		if instr.Type != InstrLine {
			continue
		}
		switch {
		case before[i] && after[i]:
			emit(Keep, instr.Line())
		case !before[i] && after[i]:
			emit(Insert, instr.Line())
		case before[i] && !after[i]:
			emit(Delete, instr.Line())
		}
	}
	if len(currentDelta.Items) > 0 {
		deltas = append(deltas, currentDelta)
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

// === Single-file repository harness

const (
	storeDir       = ".sfvc"
	storeExtension = ".weave"
)

func main() {
	if err := runCLI(os.Args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "sfvc: %s\n", err)
		os.Exit(1)
	}
}

func runCLI(args []string, stdout io.Writer) error {
	if len(args) < 2 {
		printUsage(stdout)
		return nil
	}

	switch args[1] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "init":
		return runInit(args[2:], stdout)
	case "commit":
		return runCommit(args[2:], stdout)
	case "diff":
		return runDiff(args[2:], stdout)
	case "log":
		return runLog(args[2:], stdout)
	case "show":
		return runShow(args[2:], stdout)
	case "checkout", "restore":
		return runCheckout(args[2:], stdout)
	default:
		return fmt.Errorf("unknown command %q", args[1])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage:
  sfvc init FILE [-m MESSAGE]
  sfvc commit FILE [-m MESSAGE]
  sfvc diff FILE
  sfvc log [--all] FILE
  sfvc show FILE [VERSION]
  sfvc checkout FILE VERSION
`)
}

func runInit(args []string, stdout io.Writer) error {
	args, message, err := extractMessage(args, "initial import")
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("init expects FILE")
	}

	file := args[0]
	store := storePath(file)
	if _, err := os.Stat(store); err == nil {
		return fmt.Errorf("%s already exists", store)
	} else if !os.IsNotExist(err) {
		return err
	}

	lines, err := fileLines(file)
	if err != nil {
		return err
	}
	pool := NewStringPool(nil)
	lineIDs := internLines(pool, lines)
	instructions := make([]Instruction, 0, len(lineIDs)+2)
	if len(lineIDs) > 0 {
		instructions = append(instructions, Instruction{InstrBeginInsert, 1})
		for _, lineID := range lineIDs {
			instructions = append(instructions, Instruction{InstrLine, lineID})
		}
		instructions = append(instructions, Instruction{InstrEnd, 1})
	}

	weave := &Weave{
		Head:         1,
		Instructions: instructions,
		Versions: []Version{{
			ID:          1,
			Author:      currentAuthor(),
			Description: message,
		}},
		Lines: pool.Lines(),
	}
	if err := saveWeave(file, weave); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "initialized %s at version 1\n", file)
	return nil
}

func runCommit(args []string, stdout io.Writer) error {
	args, message, err := extractMessage(args, "commit")
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return fmt.Errorf("commit expects FILE")
	}

	file := args[0]
	weave, err := loadWeave(file)
	if err != nil {
		return err
	}
	baseIDs, err := versionLineIDs(weave, weave.Head)
	if err != nil {
		return err
	}
	lines, err := fileLines(file)
	if err != nil {
		return err
	}
	pool := NewStringPool(weave.Lines)
	newIDs := internLines(pool, lines)
	deltas := DiffScript(baseIDs, newIDs)
	if !hasChanges(deltas) {
		fmt.Fprintln(stdout, "no changes")
		return nil
	}

	parent := weave.Head
	newVersion := nextVersionID(weave)
	activeSet := activeSetForVersion(weave.Versions, parent)
	instructions, err := Interleave(weave.Instructions, deltas, activeSet, newVersion)
	if err != nil {
		return err
	}
	weave.Head = newVersion
	weave.Instructions = instructions
	weave.Lines = pool.Lines()
	weave.Versions = append(weave.Versions, Version{
		ID:          newVersion,
		Parents:     []VersionID{parent},
		Author:      currentAuthor(),
		Description: message,
	})
	if err := saveWeave(file, weave); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "committed version %d\n", newVersion)
	return nil
}

func runDiff(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("diff expects FILE")
	}

	file := args[0]
	weave, err := loadWeave(file)
	if err != nil {
		return err
	}
	baseIDs, err := versionLineIDs(weave, weave.Head)
	if err != nil {
		return err
	}
	lines, err := fileLines(file)
	if err != nil {
		return err
	}
	pool := NewStringPool(weave.Lines)
	newIDs := internLines(pool, lines)
	return FormatUnifiedDiff(stdout, file, DiffScript(baseIDs, newIDs), pool.Lines(), 3)
}

func runLog(args []string, stdout io.Writer) error {
	file, all, err := parseLogArgs(args)
	if err != nil {
		return err
	}

	weave, err := loadWeave(file)
	if err != nil {
		return err
	}
	var versions []Version
	if all {
		versions = slices.Clone(weave.Versions)
		slices.Reverse(versions)
	} else {
		versions = versionHistory(weave.Versions, weave.Head)
	}
	formatLogEntries(stdout, weave, versions)
	return nil
}

func runShow(args []string, stdout io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("show expects FILE [VERSION]")
	}

	weave, err := loadWeave(args[0])
	if err != nil {
		return err
	}
	version := weave.Head
	if len(args) == 2 {
		version, err = parseVersionID(args[1])
		if err != nil {
			return err
		}
	}
	lines, err := versionLines(weave, version)
	if err != nil {
		return err
	}
	_, err = stdout.Write(formatTextLines(lines))
	return err
}

func runCheckout(args []string, stdout io.Writer) error {
	if len(args) != 2 {
		return fmt.Errorf("checkout expects FILE VERSION")
	}

	file := args[0]
	version, err := parseVersionID(args[1])
	if err != nil {
		return err
	}
	weave, err := loadWeave(file)
	if err != nil {
		return err
	}
	lines, err := versionLines(weave, version)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, formatTextLines(lines), 0o644); err != nil {
		return err
	}
	weave.Head = version
	if err := saveWeave(file, weave); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "checked out version %d\n", version)
	return nil
}

func storePath(file string) string {
	return filepath.Join(filepath.Dir(file), storeDir, filepath.Base(file)+storeExtension)
}

func loadWeave(file string) (*Weave, error) {
	data, err := os.ReadFile(storePath(file))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%s is not initialized", file)
	}
	if err != nil {
		return nil, err
	}
	var weave Weave
	if err := json.Unmarshal(data, &weave); err != nil {
		return nil, err
	}
	if err := validateVersionParents(&weave); err != nil {
		return nil, err
	}
	return &weave, nil
}

func saveWeave(file string, weave *Weave) error {
	if err := validateVersionParents(weave); err != nil {
		return err
	}
	data, err := json.MarshalIndent(weave, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	store := storePath(file)
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		return err
	}
	return os.WriteFile(store, data, 0o644)
}

func validateVersionParents(weave *Weave) error {
	versions := versionsByID(weave)
	for _, version := range weave.Versions {
		for _, parent := range version.Parents {
			if parent <= 0 {
				return fmt.Errorf("version %d has invalid parent %d", version.ID, parent)
			}
			if parent >= version.ID {
				return fmt.Errorf("version %d parent %d must be older than the child", version.ID, parent)
			}
			if _, ok := versions[parent]; !ok {
				return fmt.Errorf("version %d parent %d does not exist", version.ID, parent)
			}
		}
	}
	return nil
}

func activeSetForVersion(versions []Version, v VersionID) ActiveSet {
	activeSet := make(ActiveSet, len(versions)+1)
	stack := []VersionID{v}
	for len(stack) > 0 {
		w := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		activeSet[w] = true
		for _, p := range versions[w-1].Parents {
			if !activeSet[p] {
				stack = append(stack, p)
			}
		}
	}
	return activeSet
}

func versionHistory(versions []Version, head VersionID) []Version {
	active := activeSetForVersion(versions, head)
	var history []Version
	for _, v := range versions {
		if active[v.ID] {
			history = append(history, v)
		}
	}
	slices.Reverse(history)
	return history
}

func formatLogEntries(w io.Writer, weave *Weave, versions []Version) {
	for _, version := range versions {
		marker := "*"
		if version.ID == weave.Head {
			marker = "@"
		}
		firstLine, _, _ := strings.Cut(version.Description, "\n")
		author := version.Author
		if author == "" {
			author = "<unknown>"
		}
		versionStr := fmt.Sprintf("v%d", version.ID)
		fmt.Fprintf(w, "%s  %-4s %s\n", marker, versionStr, author)
		fmt.Fprintf(w, "│  %s\n", firstLine)
		fmt.Fprintf(w, "│\n")
	}
	fmt.Fprintf(w, "~\n")
}

func parseLogArgs(args []string) (file string, all bool, err error) {
	for _, arg := range args {
		switch arg {
		case "--all", "-a":
			all = true
		default:
			if file != "" {
				return "", false, fmt.Errorf("log expects [--all] FILE")
			}
			file = arg
		}
	}
	if file == "" {
		return "", false, fmt.Errorf("log expects [--all] FILE")
	}
	return file, all, nil
}

func versionsByID(weave *Weave) map[VersionID]Version {
	versions := make(map[VersionID]Version, len(weave.Versions))
	for _, version := range weave.Versions {
		versions[version.ID] = version
	}
	return versions
}

func highestVersionID(weave *Weave) VersionID {
	var result VersionID
	for _, version := range weave.Versions {
		result = max(result, version.ID)
	}
	for _, instr := range weave.Instructions {
		switch instr.Type {
		case InstrBeginInsert, InstrBeginDelete, InstrEnd:
			result = max(result, instr.VersionID())
		}
	}
	return result
}

func nextVersionID(weave *Weave) VersionID {
	return VersionID(len(weave.Versions) + 1)
}

func versionLineIDs(weave *Weave, version VersionID) ([]int, error) {
	if int(version) < 1 || len(weave.Versions) < int(version) {
		return nil, fmt.Errorf("version %d does not exist", version)
	}
	activeSet := activeSetForVersion(weave.Versions, version)
	mask, _, err := Reconstruct(weave.Instructions, activeSet)
	if err != nil {
		return nil, err
	}
	var ids []int
	for i, instr := range weave.Instructions {
		if mask[i] {
			ids = append(ids, instr.Line())
		}
	}
	return ids, nil
}

func versionLines(weave *Weave, version VersionID) ([]string, error) {
	ids, err := versionLineIDs(weave, version)
	if err != nil {
		return nil, err
	}
	lines := make([]string, 0, len(ids))
	for _, id := range ids {
		if id < 0 || id >= len(weave.Lines) {
			return nil, fmt.Errorf("%w: line id %d is outside the string pool", ErrBadWeave, id)
		}
		lines = append(lines, weave.Lines[id])
	}
	return lines, nil
}

func internLines(pool *StringPool, lines []string) []int {
	ids := make([]int, len(lines))
	for i, line := range lines {
		ids[i] = pool.Intern(line)
	}
	return ids
}

func fileLines(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	text := string(data)
	if text == "" {
		return nil, nil
	}
	// Remove at most one final newline
	// so that Split doesn't produce an extra empty line at the end.
	text = strings.TrimSuffix(text, "\n")
	return strings.Split(text, "\n"), nil
}

func formatTextLines(lines []string) []byte {
	var buf strings.Builder
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteRune('\n')
	}
	return []byte(buf.String())
}

func hasChanges(deltas []Delta) bool {
	return slices.ContainsFunc(deltas, func(d Delta) bool {
		return d.Action != Keep
	})
}

func parseVersionID(s string) (VersionID, error) {
	s = strings.TrimPrefix(s, "v")
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid version %q", s)
	}
	return VersionID(n), nil
}

func currentAuthor() string {
	if author := os.Getenv("SFVC_AUTHOR"); author != "" {
		return author
	}
	current, err := osuser.Current()
	if err == nil && current.Username != "" {
		return current.Username
	}
	return "unknown"
}

func extractMessage(args []string, defaultMessage string) ([]string, string, error) {
	message := defaultMessage
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m", "--message":
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s expects a message", args[i])
			}
			message = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, message, nil
}
