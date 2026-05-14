package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
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

func appendDelta(script []Delta, action Action, item int) []Delta {
	n := len(script)
	if n > 0 && script[n-1].Action == action {
		script[n-1].Items = append(script[n-1].Items, item)
		return script
	}
	return append(script, Delta{Action: action, Items: []int{item}})
}

type InstructionType int

const (
	Line InstructionType = iota
	BeginInsert
	BeginDelete
	End
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
	case Line:
		return fmt.Sprintf("%d", i.Payload)
	case BeginInsert:
		return fmt.Sprintf("^AI %d", i.Payload)
	case BeginDelete:
		return fmt.Sprintf("^AD %d", i.Payload)
	case End:
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
	Date        time.Time
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

	mask = make([]bool, len(instructions))
	openBlocksByVersion := make(map[VersionID]int)
	// activeBlocks are sorted by VersionID (ascending).
	var activeBlocks []Instruction
	activeBlock := func(v VersionID) (int, bool) {
		return slices.BinarySearchFunc(activeBlocks, v,
			func(a Instruction, target VersionID) int {
				return cmp.Compare(a.VersionID(), target)
			})
	}

	for offset, instr := range instructions {
		switch instr.Type {
		case Line:
			if len(activeBlocks) == 0 {
				return nil, nil, ErrBadWeave
			}
			top := activeBlocks[len(activeBlocks)-1]
			v := top.VersionID()
			if activeSet.Contains(v) && top.Type == BeginInsert {
				mask[offset] = true
				versions = append(versions, v)
			}
		case BeginInsert, BeginDelete:
			v := instr.VersionID()
			if _, ok := openBlocksByVersion[v]; ok {
				return nil, nil, ErrBadWeave
			}
			if instr.Type == BeginInsert || activeSet.Contains(v) {
				at, _ := activeBlock(v)
				activeBlocks = slices.Insert(activeBlocks, at, instr)
			}
			openBlocksByVersion[v] = offset
		case End:
			v := instr.VersionID()
			if _, ok := openBlocksByVersion[v]; !ok {
				return nil, nil, ErrBadWeave
			}
			at, found := activeBlock(v)
			if found {
				activeBlocks = slices.Delete(activeBlocks, at, at+1)
			}
			delete(openBlocksByVersion, v)
		default:
			return nil, nil, ErrBadWeave
		}
	}
	if len(openBlocksByVersion) > 0 {
		return nil, nil, ErrBadWeave
	}
	return mask, versions, nil
}

// Interleave extends a weave with a new delta.
//
// Returns [ErrBadWeave] if the instructions aren't a valid weave.
// Returns [ErrBadDelta] if the deltas don't apply cleanly.
func Interleave(
	instructions []Instruction,
	activeSet ActiveSet,
	deltas []Delta,
	v VersionID,
) (out []Instruction, err error) {
	mask, _, err := Reconstruct(instructions, activeSet)
	if err != nil {
		return nil, err
	}

	i, j, n, m := 0, 0, len(instructions), len(deltas)

	// consumeLines outputs instructions until it sees all expected lines in order.
	consumeLines := func(expected []int) error {
		skipped := 0
		for skipped < len(expected) {
			if i >= n {
				return ErrBadDelta
			}
			out = append(out, instructions[i])
			if mask[i] {
				if expected[skipped] != instructions[i].Line() {
					return ErrBadDelta
				}
				skipped++
			}
			i++
		}
		return nil
	}

	for i < n || j < m {
		for i < n && !mask[i] {
			out = append(out, instructions[i])
			i++
		}
		if j < m {
			delta := deltas[j]
			switch delta.Action {
			case Insert:
				out = append(out, Instruction{BeginInsert, int(v)})
				for _, item := range delta.Items {
					out = append(out, Instruction{Line, item})
				}
				out = append(out, Instruction{End, int(v)})
			case Delete:
				out = append(out, Instruction{BeginDelete, int(v)})
				if err := consumeLines(delta.Items); err != nil {
					return nil, err
				}
				out = append(out, Instruction{End, int(v)})
			case Keep:
				if err := consumeLines(delta.Items); err != nil {
					return nil, err
				}
			}
			j++
		} else if i < n {
			return nil, ErrBadDelta
		}
	}
	return out, nil
}

// ReconstructDelta extracts the changes between two active version sets.
func ReconstructDelta(
	instructions []Instruction,
	beforeSet, afterSet ActiveSet,
) (deltas []Delta, err error) {
	before, _, err := Reconstruct(instructions, beforeSet)
	if err != nil {
		return nil, err
	}
	after, _, err := Reconstruct(instructions, afterSet)
	if err != nil {
		return nil, err
	}

	for i, instr := range instructions {
		if instr.Type != Line {
			continue
		}
		switch {
		case before[i] && after[i]:
			deltas = appendDelta(deltas, Keep, instr.Line())
		case !before[i] && after[i]:
			deltas = appendDelta(deltas, Insert, instr.Line())
		case before[i] && !after[i]:
			deltas = appendDelta(deltas, Delete, instr.Line())
		}
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

// DiffScript computes the delta between two sequences.
func DiffScript(a, b []int) (script []Delta) {
	n, m := len(a), len(b)

	// Build a table of lengths of longest common subsequences of a and b.
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := range slices.Backward(a) {
		for j := range slices.Backward(b) {
			if a[i] == b[j] {
				lcs[i][j] = 1 + lcs[i+1][j+1]
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	// Traverse the LCS table and reconstruct the optimal edits.
	for i, j := 0, 0; i < n || j < m; {
		switch {
		case i < n && j < m && a[i] == b[j]:
			script = appendDelta(script, Keep, a[i])
			i++
			j++
		case i < n && (j == m || lcs[i+1][j] >= lcs[i][j+1]):
			script = appendDelta(script, Delete, a[i])
			i++
		default:
			script = appendDelta(script, Insert, b[j])
			j++
		}
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
		instructions = append(instructions, Instruction{BeginInsert, 1})
		for _, lineID := range lineIDs {
			instructions = append(instructions, Instruction{Line, lineID})
		}
		instructions = append(instructions, Instruction{End, 1})
	}

	date, err := currentDate()
	if err != nil {
		return err
	}
	weave := &Weave{
		Head:         1,
		Instructions: instructions,
		Versions: []Version{{
			ID:          1,
			Date:        date,
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

	date, err := currentDate()
	if err != nil {
		return err
	}
	parent := weave.Head
	newVersion := nextVersionID(weave)
	activeSet := activeSetForVersion(weave.Versions, parent)
	instructions, err := Interleave(weave.Instructions, activeSet, deltas, newVersion)
	if err != nil {
		return err
	}
	weave.Head = newVersion
	weave.Instructions = instructions
	weave.Lines = pool.Lines()
	weave.Versions = append(weave.Versions, Version{
		ID:          newVersion,
		Parents:     []VersionID{parent},
		Date:        date,
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
		fmt.Fprintf(w, "%s  %-4s %-10s %s\n", marker, versionStr, author, version.Date.Format("2006-01-02 15:04:05 -0700"))
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
		case BeginInsert, BeginDelete, End:
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
	current, err := user.Current()
	if err == nil && current.Username != "" {
		return current.Username
	}
	return "unknown"
}

func currentDate() (time.Time, error) {
	if date := os.Getenv("SFVC_DATE"); date != "" {
		t, err := time.Parse(time.RFC3339, date)
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid SFVC_DATE: %w", err)
		}
		return t, nil
	}
	return time.Now(), nil
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
