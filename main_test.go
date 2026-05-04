package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func checkDiffScript(t *testing.T, a, b []int, want string) {
	t.Helper()
	got := FormatScript(DiffScript(a, b))
	if got != want {
		t.Errorf("DiffScript(%v, %v):\ngot:\n%s\nwant:\n%s", a, b, got, want)
	}
}

func TestDiffScript(t *testing.T) {
	tests := []struct {
		name string
		a, b []int
		want string
	}{
		{"identical", []int{1, 2, 3}, []int{1, 2, 3}, " 1\n 2\n 3\n"},
		{"both empty", []int{}, []int{}, ``},
		{"insert all", []int{}, []int{1, 2, 3}, "+1\n+2\n+3\n"},
		{"delete all", []int{1, 2, 3}, []int{}, "-1\n-2\n-3\n"},
		{"insert at start", []int{2, 3}, []int{1, 2, 3}, "+1\n 2\n 3\n"},
		{"insert at end", []int{1, 2}, []int{1, 2, 3}, " 1\n 2\n+3\n"},
		{"delete at start", []int{1, 2, 3}, []int{2, 3}, "-1\n 2\n 3\n"},
		{"delete at end", []int{1, 2, 3}, []int{1, 2}, " 1\n 2\n-3\n"},
		{"replace middle", []int{1, 2, 3}, []int{1, 5, 3}, " 1\n-2\n+5\n 3\n"},
		{"replace all", []int{1, 2}, []int{3, 4}, "-1\n-2\n+3\n+4\n"},
		{"interleaved inserts", []int{1, 3, 5}, []int{1, 2, 3, 4, 5}, " 1\n+2\n 3\n+4\n 5\n"},
		{"single identical", []int{1}, []int{1}, " 1\n"},
		{"single replace", []int{1}, []int{2}, "-1\n+2\n"},
		{"complex", []int{4, 1, 8, 8, 8, 1, 2, 3, 4}, []int{4, 9, 8, 1, 2, 3, 5},
			" 4\n-1\n-8\n-8\n+9\n 8\n 1\n 2\n 3\n-4\n+5\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkDiffScript(t, tt.a, tt.b, tt.want)
		})
	}
}

func checkReconstruct(t *testing.T, spec string, v VersionID) {
	var wantMask []bool
	var instructions []Instruction

	for line := range strings.Lines(spec) {
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		var mark rune
		var cmd string
		var p int

		_, err := fmt.Sscanf(line, "%c %d", &mark, &p)
		if err == nil {
			switch mark {
			case ' ':
				wantMask = append(wantMask, false)
			case '*':
				wantMask = append(wantMask, true)
			default:
				t.Fatalf("unexpected marker %c on line %s", mark, line)
			}
			instructions = append(instructions, Instruction{InstrLine, p})
			continue
		}
		_, err = fmt.Sscanf(line, "  %s %d", &cmd, &p)
		if err == nil {
			wantMask = append(wantMask, false)
			var instr Instruction
			instr.Payload = p
			switch cmd {
			case "^AI":
				instr.Type = InstrBeginInsert
			case "^AD":
				instr.Type = InstrBeginDelete
			case "^AE":
				instr.Type = InstrEnd
			default:
				t.Fatalf("unsupported instruction: %s", line)
			}
			instructions = append(instructions, instr)
			continue
		}
		t.Fatalf("cannot parse line %s: %s", line, err)
	}
	activeSet := linearActiveSet(v, versionCount(instructions))
	mask, _, err := Reconstruct(instructions, activeSet)
	if err != nil {
		t.Fatalf("reconstruct failed: %s", err)
	}
	if !slices.Equal(wantMask, mask) {
		var buf strings.Builder
		for i, instr := range instructions {
			l, r := ' ', ' '
			if wantMask[i] {
				l = '*'
			}
			if mask[i] {
				r = '*'
			}
			fmt.Fprintf(&buf, "%c %c %s\n", l, r, instr)
		}
		t.Fatalf("wrong reconstruct result:\n%s", buf.String())
	}
}

func TestReconstruct(t *testing.T) {
	for _, test := range []struct {
		name    string
		version int
		weave   string
	}{
		{name: "version 1", version: 1, weave: `
  ^AI 1
* 1
  ^AD 3
* 2
  ^AI 2
  3
  ^AE 3
  4
  ^AE 2
* 5
  ^AE 1
`},
		{name: "version 2", version: 2, weave: `
  ^AI 1
* 1
  ^AD 3
* 2
  ^AI 2
* 3
  ^AE 3
* 4
  ^AE 2
* 5
  ^AE 1
`},
		{name: "version 3", version: 3, weave: `
  ^AI 1
* 1
  ^AD 3
  2
  ^AI 2
  3
  ^AE 3
* 4
  ^AE 2
* 5
  ^AE 1
`},
	} {
		t.Run(test.name, func(t *testing.T) {
			checkReconstruct(t, test.weave, VersionID(test.version))
		})
	}
}

func TestReconstructErrors(t *testing.T) {
	tests := []struct {
		name    string
		weave   string
		wantMsg string
	}{
		{"bare line", "1\n", "line 0"},
		{"unmatched end", "^AE 1\n", "line 0"},
		{"unclosed insert", "^AI 1\n1\n", "opened at line 0"},
		{"duplicate begin", "^AI 1\n^AD 1\n1\n^AE 1\n^AE 1\n", "previous beginning was at line 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instructions, err := parseWeave(tt.weave)
			if err != nil {
				t.Fatalf("parse weave: %s", err)
			}
			_, _, err = Reconstruct(instructions, linearActiveSet(2, versionCount(instructions)))
			if !errors.Is(err, ErrBadWeave) {
				t.Fatalf("got %v, want ErrBadWeave", err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("got %q, want message containing %q", err, tt.wantMsg)
			}
		})
	}
}

func TestReconstructTreatsMissingActiveSetVersionsAsInactive(t *testing.T) {
	weave, err := parseWeave(`
^AI 1
1
^AI 2
2
^AE 2
3
^AE 1
`)
	if err != nil {
		t.Fatalf("parse weave: %s", err)
	}
	mask, _, err := Reconstruct(weave, ActiveSet{true, true})
	if err != nil {
		t.Fatalf("reconstruct: %s", err)
	}
	var got []int
	for i, instr := range weave {
		if mask[i] {
			got = append(got, instr.Line())
		}
	}
	if want := []int{1, 3}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReconstructDeltaErrorsOnUnclosedTargetSpan(t *testing.T) {
	weave, err := parseWeave(`
^AI 1
1
`)
	if err != nil {
		t.Fatalf("parse weave: %s", err)
	}
	_, err = ReconstructDelta(weave, 1, linearActiveSet(1, versionCount(weave)))
	if !errors.Is(err, ErrBadWeave) {
		t.Fatalf("got %v, want ErrBadWeave", err)
	}
	if !strings.Contains(err.Error(), "opened at line 0") {
		t.Fatalf("got %q, want message containing opening line", err)
	}
}

func parseDeltas(s string) ([]Delta, error) {
	var deltas []Delta
	lineNumber := 0
	for line := range strings.Lines(s) {
		lineNumber++
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}
		var marker rune
		var lineIndex int
		_, err := fmt.Sscanf(line, "%c %d", &marker, &lineIndex)
		if err != nil {
			return nil, fmt.Errorf("failed to parse line %d: %s", lineNumber, line)
		}
		var action Action
		switch marker {
		case ' ':
			action = Keep
		case '+':
			action = Insert
		case '-':
			action = Delete
		default:
			return nil, fmt.Errorf("line %d (%s): invalid marker %c", lineNumber, line, marker)
		}
		n := len(deltas)
		if n > 0 && deltas[n-1].Action == action {
			deltas[n-1].Items = append(deltas[n-1].Items, lineIndex)
		} else {
			deltas = append(deltas, Delta{Action: action, Items: []int{lineIndex}})
		}
	}
	return deltas, nil
}

func parseDeltasWithLines(s string) (deltas []Delta, lines []string, err error) {
	s = strings.TrimPrefix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	lineNumber := 0
	stringPool := NewStringPool(nil)
	for line := range strings.Lines(s) {
		lineNumber++
		line = strings.TrimSuffix(line, "\n")

		var action Action
		var content string
		if strings.HasPrefix(line, "+") {
			action = Insert
			content = line[1:]
		} else if strings.HasPrefix(line, "-") {
			action = Delete
			content = line[1:]
		} else if strings.HasPrefix(line, " ") {
			action = Keep
			content = line[1:]
		} else if len(line) == 0 {
			action = Keep
			content = ""
		} else {
			return nil, nil, fmt.Errorf("failed to parse line %d: %s", lineNumber, line)
		}

		lineIndex := stringPool.Intern(content)
		n := len(deltas)
		if n > 0 && deltas[n-1].Action == action {
			deltas[n-1].Items = append(deltas[n-1].Items, lineIndex)
		} else {
			deltas = append(deltas, Delta{Action: action, Items: []int{lineIndex}})
		}
	}
	return deltas, stringPool.Lines(), nil
}

func versionCount(weave []Instruction) int {
	result := 0
	for _, instr := range weave {
		switch instr.Type {
		case InstrBeginInsert, InstrBeginDelete:
			result = max(result, instr.Payload)

		}
	}
	return result
}

func parseWeave(s string) ([]Instruction, error) {
	var instructions []Instruction
	lineNumber := 0
	for line := range strings.Lines(s) {
		lineNumber++
		if len(strings.TrimSpace(line)) == 0 {
			continue
		}

		var cmd string
		var payload int
		_, err := fmt.Sscanf(line, "%d", &payload)
		if err == nil {
			instructions = append(instructions, Instruction{InstrLine, payload})
			continue
		}
		_, err = fmt.Sscanf(line, "%s %d", &cmd, &payload)
		if err != nil {
			return nil, fmt.Errorf("cannot parse line %d (%s)", lineNumber, line)
		}
		var instr Instruction
		instr.Payload = payload
		switch cmd {
		case "^AI":
			instr.Type = InstrBeginInsert
		case "^AD":
			instr.Type = InstrBeginDelete
		case "^AE":
			instr.Type = InstrEnd
		default:
			return nil, fmt.Errorf("unsupported instruction on line %d: %s", lineNumber, cmd)
		}
		instructions = append(instructions, instr)
	}
	return instructions, nil
}

func linearActiveSet(v VersionID, count int) ActiveSet {
	activeSet := make(ActiveSet, max(int(v), count)+1)
	for i := range int(v) + 1 {
		activeSet[i] = true
	}
	return activeSet
}

func checkInterleave(t *testing.T, baseStr, deltasStr, resultStr string, newV int) {
	baseW, err := parseWeave(baseStr)
	if err != nil {
		t.Fatalf("failed to parse base weave: %s", err)
	}
	deltas, err := parseDeltas(deltasStr)
	if err != nil {
		t.Fatalf("failed to parse deltas: %s", err)
	}
	resultW, err := parseWeave(resultStr)
	if err != nil {
		t.Fatalf("failed to parse result weave: %s", err)
	}
	activeSet := linearActiveSet(VersionID(newV), versionCount(resultW))

	interleaved, err := Interleave(baseW, deltas, activeSet, VersionID(newV))
	if err != nil {
		t.Fatalf("interleave failed: %s", err)
	}
	if !slices.Equal(resultW, interleaved) {
		t.Fatalf("Expected:\n%sGot:\n%s", FormatWeave(resultW), FormatWeave(interleaved))
	}

	reconstructedDelta, err := ReconstructDelta(resultW, VersionID(newV), activeSet)
	if !slices.EqualFunc(reconstructedDelta, deltas, EqualDeltas) {
		t.Fatalf("delta reconstruction failed:\nExpected:\n%sGot:\n%s", FormatScript(deltas), FormatScript(reconstructedDelta))
	}
}

func TestInterleave(t *testing.T) {
	tests := []struct {
		name   string
		base   string
		delta  string
		result string
		newV   int
	}{
		{
			name:  "insert into empty file",
			base:  "",
			delta: "+ 1\n+ 2\n+ 3\n",
			result: `
^AI 1
1
2
3
^AE 1
`, newV: 1,
		},
		{
			name: "insert in the middle",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "  1\n  2\n+ 4\n+ 5\n  3\n",
			result: `
^AI 1
1
2
^AI 2
4
5
^AE 2
3
^AE 1
`, newV: 2,
		},
		{
			name: "delete across adjacent deltas",
			base: `
^AI 1
1
2
^AI 2
4
5
^AE 2
3
^AE 1
`, delta: "  1\n- 2\n- 4\n  5\n  3\n",
			result: `
^AI 1
1
^AD 3
2
^AI 2
4
^AE 3
5
^AE 2
3
^AE 1
`, newV: 3,
		},
		{
			name: "insert at the beginning",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "+ 10\n+ 11\n  1\n  2\n  3\n",
			result: `
^AI 1
^AI 2
10
11
^AE 2
1
2
3
^AE 1
`, newV: 2,
		},
		{
			name: "append at the end",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "  1\n  2\n  3\n+ 10\n+ 11\n",
			result: `
^AI 1
1
2
3
^AE 1
^AI 2
10
11
^AE 2
`, newV: 2,
		},
		{
			name: "delete all lines",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "- 1\n- 2\n- 3\n",
			result: `
^AI 1
^AD 2
1
2
3
^AE 2
^AE 1
`, newV: 2,
		},
		{
			name: "replace a line",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "  1\n- 2\n+ 20\n  3\n",
			result: `
^AI 1
1
^AD 2
2
^AE 2
^AI 2
20
^AE 2
3
^AE 1
`, newV: 2,
		},
		{
			name: "multiple scattered insertions",
			base: `
^AI 1
1
2
3
4
5
^AE 1
`, delta: "+ 10\n  1\n+ 20\n  2\n  3\n+ 30\n+ 31\n  4\n  5\n",
			result: `
^AI 1
^AI 2
10
^AE 2
1
^AI 2
20
^AE 2
2
3
^AI 2
30
31
^AE 2
4
5
^AE 1
`, newV: 2,
		},
		{
			name: "fourth version on top of deleted regions",
			base: `
^AI 1
1
^AD 3
2
^AI 2
4
^AE 3
5
^AE 2
3
^AE 1
`, delta: "  1\n+ 10\n  5\n- 3\n",
			result: `
^AI 1
1
^AD 3
2
^AI 2
4
^AE 3
^AI 4
10
^AE 4
5
^AE 2
^AD 4
3
^AE 4
^AE 1
`, newV: 4,
		},
		{
			name: "nested insertions",
			base: `
^AI 1
1
^AI 2
2
^AE 2
3
^AE 1
`, delta: "  1\n+ 10\n  2\n  3\n",
			result: `
^AI 1
1
^AI 2
^AI 3
10
^AE 3
2
^AE 2
3
^AE 1
`, newV: 3,
		},
		{
			name: "delete a previously inserted line",
			base: `
^AI 1
1
^AI 2
2
^AE 2
3
^AE 1
`, delta: "  1\n- 2\n  3\n",
			result: `
^AI 1
1
^AI 2
^AD 3
2
^AE 3
^AE 2
3
^AE 1
`, newV: 3,
		},
		{
			name: "completely replace content",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "- 1\n- 2\n- 3\n+ 10\n+ 20\n+ 30\n",
			result: `
^AI 1
^AD 2
1
2
3
^AE 2
^AE 1
^AI 2
10
20
30
^AE 2
`, newV: 2,
		},
		{
			name: "multiple non-contiguous deletions",
			base: `
^AI 1
1
2
3
4
5
^AE 1
`, delta: "  1\n- 2\n  3\n- 4\n  5\n",
			result: `
^AI 1
1
^AD 2
2
^AE 2
3
^AD 2
4
^AE 2
5
^AE 1
`, newV: 2,
		},
		{
			name: "simultaneous insert and delete",
			base: `
^AI 1
1
2
3
^AE 1
`, delta: "  1\n+ 10\n  2\n- 3\n",
			result: `
^AI 1
1
^AI 2
10
^AE 2
2
^AD 2
3
^AE 2
^AE 1
`, newV: 2,
		},
		{
			name:   "empty delta on empty base",
			base:   "",
			delta:  "",
			result: "",
			newV:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkInterleave(t, tt.base, tt.delta, tt.result, tt.newV)
		})
	}
}

func TestInterleaveErrors(t *testing.T) {
	baseWeave := `
^AI 1
1
2
3
^AE 1
`
	tests := []struct {
		name  string
		delta string
		newV  int
	}{
		{
			name:  "incomplete delta: only covers first line",
			delta: "  1\n",
			newV:  2,
		},
		{
			name:  "incomplete delta: empty delta on non-empty base",
			delta: "",
			newV:  2,
		},
		{
			name:  "mismatched keep line",
			delta: "  1\n  9\n  3\n",
			newV:  2,
		},
		{
			name:  "mismatched delete line",
			delta: "  1\n- 9\n  3\n",
			newV:  2,
		},
		{
			name:  "delta longer than base",
			delta: "  1\n  2\n  3\n  4\n",
			newV:  2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseW, err := parseWeave(baseWeave)
			if err != nil {
				t.Fatalf("parse base weave: %s", err)
			}
			deltas, err := parseDeltas(tt.delta)
			if err != nil {
				t.Fatalf("parse deltas: %s", err)
			}
			activeSet := linearActiveSet(VersionID(tt.newV), tt.newV)
			_, err = Interleave(baseW, deltas, activeSet, VersionID(tt.newV))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !errors.Is(err, ErrBadDelta) {
				t.Fatalf("expected ErrBadDelta, got: %s", err)
			}
		})
	}
}

func checkUnifiedDiff(t *testing.T, filename, deltaStr, diffStr string, context int) {
	t.Helper()
	deltas, lines, err := parseDeltasWithLines(deltaStr)
	if err != nil {
		t.Fatalf("failed to parse deltas: %s", err)
	}
	var buf strings.Builder
	if err := FormatUnifiedDiff(&buf, filename, deltas, lines, context); err != nil {
		t.Fatalf("unexpected io error: %s", err)
	}
	formattedDiff := buf.String()
	expectedDiff := strings.TrimPrefix(diffStr, "\n")
	// Normalize: in expected string, empty lines between diff markers
	// should be treated as " \n" (context blank lines).
	expectedDiff = strings.ReplaceAll(expectedDiff, "\n\n", "\n \n")
	if formattedDiff != expectedDiff {
		t.Fatalf("unified diff doesn't match.\nExpected:\n======\n%s======\nGot:\n======\n%s======\n", expectedDiff, formattedDiff)
	}
}

func TestUnifiedDiff(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		delta    string
		diff     string
		context  int
	}{
		{
			name:     "no changes",
			filename: "test.txt",
			delta: `
 line1
 line2
 line3
`,
			diff: `
--- test.txt
+++ test.txt
`,
			context: 1,
		},
		{
			name:     "basic replacement",
			filename: "main.c",
			delta: `
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`,
			diff: `
--- main.c
+++ main.c
@@ -3,3 +3,3 @@
 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`,
			context: 1,
		},
		{
			name:     "larger context",
			filename: "main.c",
			delta: `
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`,
			diff: `
--- main.c
+++ main.c
@@ -1,5 +1,5 @@
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`,
			context: 3,
		},
		{
			name:     "more deletes than inserts",
			filename: "main.c",
			delta: `
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
-  return 0;
+  printf("Goodbye, cruel world!");
 }
`,
			diff: `
--- main.c
+++ main.c
@@ -1,6 +1,5 @@
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
-  return 0;
+  printf("Goodbye, cruel world!");
 }
`,
			context: 3,
		},
		{
			name:     "multiple separate hunks",
			filename: "main.c",
			delta: `
 #include <stdio.h>

 int main() {
   for (int i = 0; i < 10; i++) {
-    printf("Hello, World!");
+    printf("Goodbye, cruel world!");
   }
   // comment
   // another comment
+  return 0;
 }
`,
			diff: `
--- main.c
+++ main.c
@@ -4,3 +4,3 @@
   for (int i = 0; i < 10; i++) {
-    printf("Hello, World!");
+    printf("Goodbye, cruel world!");
   }
@@ -8,2 +8,3 @@
   // another comment
+  return 0;
 }
`,
			context: 1,
		},
		{
			name:     "pure insertion",
			filename: "file.txt",
			delta: `
 line1
+inserted1
+inserted2
 line2
 line3
`,
			diff: `
--- file.txt
+++ file.txt
@@ -1,3 +1,5 @@
 line1
+inserted1
+inserted2
 line2
 line3
`,
			context: 1,
		},
		{
			name:     "pure deletion",
			filename: "file.txt",
			delta: `
 line1
-deleted1
-deleted2
 line2
`,
			diff: `
--- file.txt
+++ file.txt
@@ -1,4 +1,2 @@
 line1
-deleted1
-deleted2
 line2
`,
			context: 1,
		},
		{
			name:     "change at beginning",
			filename: "file.txt",
			delta: `
-old_first_line
+new_first_line
 line2
 line3
`,
			diff: `
--- file.txt
+++ file.txt
@@ -1,3 +1,3 @@
-old_first_line
+new_first_line
 line2
 line3
`,
			context: 1,
		},
		{
			name:     "change at end",
			filename: "file.txt",
			delta: `
 line1
 line2
-old_last_line
+new_last_line
`,
			diff: `
--- file.txt
+++ file.txt
@@ -2,2 +2,2 @@
 line2
-old_last_line
+new_last_line
`,
			context: 1,
		},
		{
			name:     "hunks merge due to small gap",
			filename: "file.txt",
			delta: `
-line1
+changed1
 gap
-line3
+changed3
`,
			diff: `
--- file.txt
+++ file.txt
@@ -1,3 +1,3 @@
-line1
+changed1
 gap
-line3
+changed3
`,
			context: 1,
		},
		{
			name:     "zero context",
			filename: "file.txt",
			delta: `
 line1
 line2
-deleted
 line3
`,
			diff: `
--- file.txt
+++ file.txt
@@ -3,1 +3,0 @@
-deleted
`,
			context: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkUnifiedDiff(t, tt.filename, tt.delta, tt.diff, tt.context)
		})
	}
}

func makeActiveSet(ancestors []int, maxVersion int) ActiveSet {
	as := make(ActiveSet, maxVersion+1)
	for _, v := range ancestors {
		as[v] = true
	}
	return as
}

func reconstructLines(t *testing.T, weave []Instruction, as ActiveSet) []int {
	t.Helper()
	mask, _, err := Reconstruct(weave, as)
	if err != nil {
		t.Fatalf("Reconstruct(as=%v): %s", as, err)
	}
	var lines []int
	for i, instr := range weave {
		if mask[i] {
			lines = append(lines, instr.Line())
		}
	}
	return lines
}

func TestNonLinearHistory(t *testing.T) {
	// Version graph:
	//   1 → 2 → 3
	//   1 → 4 → 5

	as := func(ancestors ...int) ActiveSet {
		return makeActiveSet(ancestors, 5)
	}

	interleave := func(weave []Instruction, deltaStr string, activeSet ActiveSet, newV int) []Instruction {
		t.Helper()
		deltas, err := parseDeltas(deltaStr)
		if err != nil {
			t.Fatalf("parse deltas for v%d: %s", newV, err)
		}
		result, err := Interleave(weave, deltas, activeSet, VersionID(newV))
		if err != nil {
			t.Fatalf("interleave v%d: %s", newV, err)
		}
		return result
	}

	// v1: creates lines 1, 2, 3
	weave := interleave(nil, `
+ 1
+ 2
+ 3
`, as(0, 1), 1)

	// v2: inserts 10 after 1
	weave = interleave(weave, `
  1
+ 10
  2
  3
`, as(0, 1, 2), 2)

	// v4: deletes 2 (from v1, independent branch)
	weave = interleave(weave, `
  1
- 2
  3
`, as(0, 1, 4), 4)

	// v3: deletes 10 (from v2)
	weave = interleave(weave, `
  1
- 10
  2
  3
`, as(0, 1, 2, 3), 3)

	// v5: inserts 20 before 3 (from v4)
	weave = interleave(weave, `
  1
+ 20
  3
`, as(0, 1, 4, 5), 5)

	wantWeave, err := parseWeave(`
^AI 1
1
^AI 2
^AD 3
10
^AE 3
^AE 2
^AD 4
2
^AE 4
^AI 5
20
^AE 5
3
^AE 1
`)
	if err != nil {
		t.Fatalf("parse expected weave: %s", err)
	}
	if !slices.Equal(weave, wantWeave) {
		t.Fatalf("weave mismatch:\nExpected:\n%sGot:\n%s", FormatWeave(wantWeave), FormatWeave(weave))
	}

	// Verify reconstruction for all versions.
	t.Run("reconstruct", func(t *testing.T) {
		tests := []struct {
			name      string
			v         VersionID
			ancestors []int
			want      []int
		}{
			{"v1", 1, []int{0, 1}, []int{1, 2, 3}},
			{"v2", 2, []int{0, 1, 2}, []int{1, 10, 2, 3}},
			{"v3", 3, []int{0, 1, 2, 3}, []int{1, 2, 3}},
			{"v4", 4, []int{0, 1, 4}, []int{1, 3}},
			{"v5", 5, []int{0, 1, 4, 5}, []int{1, 20, 3}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := reconstructLines(t, weave, as(tt.ancestors...))
				if !slices.Equal(got, tt.want) {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	// Verify delta reconstruction.
	t.Run("reconstruct_delta", func(t *testing.T) {
		checkDelta := func(name string, v VersionID, ancestors []int, wantStr string) {
			t.Run(name, func(t *testing.T) {
				t.Helper()
				wantDeltas, err := parseDeltas(wantStr)
				if err != nil {
					t.Fatalf("parse expected deltas: %s", err)
				}
				got, err := ReconstructDelta(weave, v, as(ancestors...))
				if err != nil {
					t.Fatalf("error: %s", err)
				}
				if !slices.EqualFunc(got, wantDeltas, EqualDeltas) {
					t.Fatalf("got:\n%swant:\n%s", FormatScript(got), FormatScript(wantDeltas))
				}
			})
		}
		checkDelta("v2", 2, []int{0, 1, 2}, "  1\n+ 10\n  2\n  3\n")
		checkDelta("v3", 3, []int{0, 1, 2, 3}, "  1\n- 10\n  2\n  3\n")
		checkDelta("v4", 4, []int{0, 1, 4}, "  1\n- 2\n  3\n")
		checkDelta("v5", 5, []int{0, 1, 4, 5}, "  1\n+ 20\n  3\n")
	})
}

func TestNonLinearConflictingDeletes(t *testing.T) {
	// Version graph:
	//   1 → 2   (delete line 2)
	//   1 → 3   (also delete line 2, independently)
	//
	// Both branches independently delete the same line.

	as := func(ancestors ...int) ActiveSet {
		return makeActiveSet(ancestors, 3)
	}

	interleave := func(weave []Instruction, deltaStr string, activeSet ActiveSet, newV int) []Instruction {
		t.Helper()
		deltas, err := parseDeltas(deltaStr)
		if err != nil {
			t.Fatalf("parse deltas for v%d: %s", newV, err)
		}
		result, err := Interleave(weave, deltas, activeSet, VersionID(newV))
		if err != nil {
			t.Fatalf("interleave v%d: %s", newV, err)
		}
		return result
	}

	weave := interleave(nil, "+ 1\n+ 2\n+ 3\n", as(0, 1), 1)
	weave = interleave(weave, "  1\n- 2\n  3\n", as(0, 1, 2), 2)
	weave = interleave(weave, "  1\n- 2\n  3\n", as(0, 1, 3), 3)

	wantWeave, err := parseWeave(`
^AI 1
1
^AD 2
^AD 3
2
^AE 3
^AE 2
3
^AE 1
`)
	if err != nil {
		t.Fatalf("parse expected weave: %s", err)
	}
	if !slices.Equal(weave, wantWeave) {
		t.Fatalf("weave mismatch:\nExpected:\n%sGot:\n%s", FormatWeave(wantWeave), FormatWeave(weave))
	}

	t.Run("reconstruct", func(t *testing.T) {
		tests := []struct {
			name      string
			ancestors []int
			want      []int
		}{
			{"v1", []int{0, 1}, []int{1, 2, 3}},
			{"v2", []int{0, 1, 2}, []int{1, 3}},
			{"v3", []int{0, 1, 3}, []int{1, 3}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := reconstructLines(t, weave, as(tt.ancestors...))
				if !slices.Equal(got, tt.want) {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("reconstruct_delta", func(t *testing.T) {
		checkDelta := func(name string, v VersionID, ancestors []int, wantStr string) {
			t.Run(name, func(t *testing.T) {
				t.Helper()
				wantDeltas, err := parseDeltas(wantStr)
				if err != nil {
					t.Fatalf("parse expected deltas: %s", err)
				}
				got, err := ReconstructDelta(weave, v, as(ancestors...))
				if err != nil {
					t.Fatalf("error: %s", err)
				}
				if !slices.EqualFunc(got, wantDeltas, EqualDeltas) {
					t.Fatalf("got:\n%swant:\n%s", FormatScript(got), FormatScript(wantDeltas))
				}
			})
		}
		checkDelta("v2", 2, []int{0, 1, 2}, "  1\n- 2\n  3\n")
		checkDelta("v3", 3, []int{0, 1, 3}, "  1\n- 2\n  3\n")
	})
}

func TestNonLinearInsertAtDeletedPosition(t *testing.T) {
	// Version graph:
	//   1 → 2   (replace line 2 with 20)
	//   1 → 3   (delete line 2)
	//
	// One branch replaces a line, the other just deletes it.

	as := func(ancestors ...int) ActiveSet {
		return makeActiveSet(ancestors, 3)
	}

	interleave := func(weave []Instruction, deltaStr string, activeSet ActiveSet, newV int) []Instruction {
		t.Helper()
		deltas, err := parseDeltas(deltaStr)
		if err != nil {
			t.Fatalf("parse deltas for v%d: %s", newV, err)
		}
		result, err := Interleave(weave, deltas, activeSet, VersionID(newV))
		if err != nil {
			t.Fatalf("interleave v%d: %s", newV, err)
		}
		return result
	}

	weave := interleave(nil, "+ 1\n+ 2\n+ 3\n", as(0, 1), 1)
	weave = interleave(weave, "  1\n- 2\n+ 20\n  3\n", as(0, 1, 2), 2)
	weave = interleave(weave, "  1\n- 2\n  3\n", as(0, 1, 3), 3)

	wantWeave, err := parseWeave(`
^AI 1
1
^AD 2
^AD 3
2
^AE 3
^AE 2
^AI 2
20
^AE 2
3
^AE 1
`)
	if err != nil {
		t.Fatalf("parse expected weave: %s", err)
	}
	if !slices.Equal(weave, wantWeave) {
		t.Fatalf("weave mismatch:\nExpected:\n%sGot:\n%s", FormatWeave(wantWeave), FormatWeave(weave))
	}

	t.Run("reconstruct", func(t *testing.T) {
		tests := []struct {
			name      string
			v         VersionID
			ancestors []int
			want      []int
		}{
			{"v1", 1, []int{0, 1}, []int{1, 2, 3}},
			{"v2", 2, []int{0, 1, 2}, []int{1, 20, 3}},
			{"v3", 3, []int{0, 1, 3}, []int{1, 3}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := reconstructLines(t, weave, as(tt.ancestors...))
				if !slices.Equal(got, tt.want) {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("reconstruct_delta", func(t *testing.T) {
		checkDelta := func(name string, v VersionID, ancestors []int, wantStr string) {
			t.Run(name, func(t *testing.T) {
				t.Helper()
				wantDeltas, err := parseDeltas(wantStr)
				if err != nil {
					t.Fatalf("parse expected deltas: %s", err)
				}
				got, err := ReconstructDelta(weave, v, as(ancestors...))
				if err != nil {
					t.Fatalf("error: %s", err)
				}
				if !slices.EqualFunc(got, wantDeltas, EqualDeltas) {
					t.Fatalf("got:\n%swant:\n%s", FormatScript(got), FormatScript(wantDeltas))
				}
			})
		}
		checkDelta("v2", 2, []int{0, 1, 2}, "  1\n- 2\n+ 20\n  3\n")
		checkDelta("v3", 3, []int{0, 1, 3}, "  1\n- 2\n  3\n")
	})
}
