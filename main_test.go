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
	checkDiffScript(t, []int{1, 2, 3}, []int{1, 2, 3}, ` 1
 2
 3
`)
	checkDiffScript(t, []int{}, []int{}, ``)
	checkDiffScript(t, []int{}, []int{1, 2, 3}, `+1
+2
+3
`)
	checkDiffScript(t, []int{1, 2, 3}, []int{}, `-1
-2
-3
`)
	checkDiffScript(t, []int{2, 3}, []int{1, 2, 3}, `+1
 2
 3
`)
	checkDiffScript(t, []int{1, 2}, []int{1, 2, 3}, ` 1
 2
+3
`)
	checkDiffScript(t, []int{1, 2, 3}, []int{2, 3}, `-1
 2
 3
`)
	checkDiffScript(t, []int{1, 2, 3}, []int{1, 2}, ` 1
 2
-3
`)
	checkDiffScript(t, []int{1, 2, 3}, []int{1, 5, 3}, ` 1
-2
+5
 3
`)
	checkDiffScript(t, []int{1, 2}, []int{3, 4}, `-1
-2
+3
+4
`)
	checkDiffScript(t, []int{1, 3, 5}, []int{1, 2, 3, 4, 5}, ` 1
+2
 3
+4
 5
`)
	checkDiffScript(t, []int{1}, []int{1}, ` 1
`)
	checkDiffScript(t, []int{1}, []int{2}, `-1
+2
`)
	checkDiffScript(t, []int{4, 1, 8, 8, 8, 1, 2, 3, 4}, []int{4, 9, 8, 1, 2, 3, 5}, ` 4
-1
-8
-8
+9
 8
 1
 2
 3
-4
+5
`)
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
			if cmd == "^AI" {
				instr.Type = InstrBeginInsert
			} else if cmd == "^AD" {
				instr.Type = InstrBeginDelete
			} else if cmd == "^AE" {
				instr.Type = InstrEnd
			} else {
				t.Fatalf("unsupported instruction: %s", line)
			}
			instructions = append(instructions, instr)
			continue
		}
		t.Fatalf("cannot parse line %s: %s", line, err)
	}
	mask, _, err := Reconstruct(instructions, VersionGraph{}, v)
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
	lineNumber := 0
	stringPool := NewStringPool(nil)
	for line := range strings.Lines(s) {
		lineNumber++
		line = strings.TrimSuffix(line, "\n")
		if len(line) == 0 {
			continue
		}

		var action Action
		if strings.HasPrefix(line, "+") {
			action = Insert
		} else if strings.HasPrefix(line, "-") {
			action = Delete
		} else if strings.HasPrefix(line, " ") {
			action = Keep
		} else {
			return nil, nil, fmt.Errorf("failed to parse line %d: %s", lineNumber, line)
		}

		lineIndex := stringPool.Intern(line[1:])
		n := len(deltas)
		if n > 0 && deltas[n-1].Action == action {
			deltas[n-1].Items = append(deltas[n-1].Items, lineIndex)
		} else {
			deltas = append(deltas, Delta{Action: action, Items: []int{lineIndex}})
		}
	}
	return deltas, stringPool.Lines(), nil
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

func checkInterleave(t *testing.T, baseStr, deltasStr, resultStr string, baseV, newV int) {
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
	var g VersionGraph

	interleaved, err := Interleave(baseW, g, deltas, VersionID(baseV), VersionID(newV))
	if err != nil {
		t.Fatalf("interleave failed: %s", err)
	}
	if !slices.Equal(resultW, interleaved) {
		t.Fatalf("Expected:\n%sGot:\n%s", FormatWeave(resultW), FormatWeave(interleaved))
	}

	reconstructedDelta, err := ReconstructDelta(resultW, g, VersionID(newV))
	if !slices.EqualFunc(reconstructedDelta, deltas, EqualDeltas) {
		t.Fatalf("delta reconstruction failed:\nExpected:\n%sGot:\n%s", FormatScript(deltas), FormatScript(reconstructedDelta))
	}
}

func TestInterleave(t *testing.T) {
	// Add a few lines to an empty file.
	checkInterleave(t, "", `
+ 1
+ 2
+ 3
`, `
^AI 1
1
2
3
^AE 1
`, 0, 1)
	// Insert two lines in the middle of a file.
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
  1
  2
+ 4
+ 5
  3
`, `
^AI 1
1
2
^AI 2
4
5
^AE 2
3
^AE 1
`, 1, 2)
	// Delete two lines that belong to adjacent deltas.
	checkInterleave(t, `
^AI 1
1
2
^AI 2
4
5
^AE 2
3
^AE 1
`, `
  1
- 2
- 4
  5
  3
`, `
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
`, 2, 3)

	// Insert at the beginning of an existing version.
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
+ 10
+ 11
  1
  2
  3
`, `
^AI 1
^AI 2
10
11
^AE 2
1
2
3
^AE 1
`, 1, 2)

	// Append at the end of an existing version.
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
  1
  2
  3
+ 10
+ 11
`, `
^AI 1
1
2
3
^AE 1
^AI 2
10
11
^AE 2
`, 1, 2)

	// Delete all lines.
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
- 1
- 2
- 3
`, `
^AI 1
^AD 2
1
2
3
^AE 2
^AE 1
`, 1, 2)

	// Replace a line (delete followed by insert at the same position).
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
  1
- 2
+ 20
  3
`, `
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
`, 1, 2)

	// Multiple scattered insertions between kept lines.
	checkInterleave(t, `
^AI 1
1
2
3
4
5
^AE 1
`, `
+ 10
  1
+ 20
  2
  3
+ 30
+ 31
  4
  5
`, `
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
`, 1, 2)

	// Fourth version on top of a weave with deleted regions.
	// Version 3 sees: 1, 5, 3 (lines 2 and 4 were deleted by v3).
	// Delta: keep 1, insert 10, keep 5, delete 3.
	// The insert lands after the inactive ^AD/^AE block because the
	// algorithm drains non-masked instructions before processing deltas.
	checkInterleave(t, `
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
`, `
  1
+ 10
  5
- 3
`, `
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
`, 3, 4)

	// Nested insertions: v2 inserts inside v1, v3 inserts inside v2.
	checkInterleave(t, `
^AI 1
1
^AI 2
2
^AE 2
3
^AE 1
`, `
  1
+ 10
  2
  3
`, `
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
`, 2, 3)

	// Delete a line that was inserted by a previous version.
	checkInterleave(t, `
^AI 1
1
^AI 2
2
^AE 2
3
^AE 1
`, `
  1
- 2
  3
`, `
^AI 1
1
^AI 2
^AD 3
2
^AE 3
^AE 2
3
^AE 1
`, 2, 3)

	// Completely replace content: delete all old, insert all new.
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
- 1
- 2
- 3
+ 10
+ 20
+ 30
`, `
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
`, 1, 2)

	// Interleave with multiple deletions spanning non-contiguous regions.
	// Base has: 1 2 3 4 5. Delete 2 and 4.
	checkInterleave(t, `
^AI 1
1
2
3
4
5
^AE 1
`, `
  1
- 2
  3
- 4
  5
`, `
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
`, 1, 2)

	// Simultaneous insert and delete at different positions.
	// Base: 1 2 3. Delta: insert X before 2, delete 3.
	checkInterleave(t, `
^AI 1
1
2
3
^AE 1
`, `
  1
+ 10
  2
- 3
`, `
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
`, 1, 2)

	// Empty delta on empty base (no-op).
	checkInterleave(t, ``, ``, ``, 0, 1)

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
		baseV int
		newV  int
	}{
		{
			name:  "incomplete delta: only covers first line",
			delta: "  1\n",
			baseV: 1, newV: 2,
		},
		{
			name:  "incomplete delta: empty delta on non-empty base",
			delta: "",
			baseV: 1, newV: 2,
		},
		{
			name:  "mismatched keep line",
			delta: "  1\n  9\n  3\n",
			baseV: 1, newV: 2,
		},
		{
			name:  "mismatched delete line",
			delta: "  1\n- 9\n  3\n",
			baseV: 1, newV: 2,
		},
		{
			name:  "delta longer than base",
			delta: "  1\n  2\n  3\n  4\n",
			baseV: 1, newV: 2,
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
			_, err = Interleave(baseW, VersionGraph{}, deltas, VersionID(tt.baseV), VersionID(tt.newV))
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
	// All lines are kept - no changes. Should produce just headers.
	checkUnifiedDiff(t, "test.txt", `
 line1
 line2
 line3
`, `
--- test.txt
+++ test.txt
`, 1)

	// Basic replacement with minimal context.
	checkUnifiedDiff(t, "main.c", `
 #include <stdio.h>
 
 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`, `
--- main.c
+++ main.c
@@ -3,3 +3,3 @@
 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`, 1)

	// Same change with larger context.
	checkUnifiedDiff(t, "main.c", `
 #include <stdio.h>
 
 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`, `
--- main.c
+++ main.c
@@ -1,5 +1,5 @@
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
+  printf("Goodbye, cruel world!");
 }
`, 3)

	// Deleting more lines than inserting.
	checkUnifiedDiff(t, "main.c", `
 #include <stdio.h>
 
 int main() {
-  printf("Hello, World!");
-  return 0;
+  printf("Goodbye, cruel world!");
 }
`, `
--- main.c
+++ main.c
@@ -1,6 +1,5 @@
 #include <stdio.h>

 int main() {
-  printf("Hello, World!");
-  return 0;
+  printf("Goodbye, cruel world!");
 }
`, 3)

	// Multiple separate hunks.
	checkUnifiedDiff(t, "main.c", `
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
`, `
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
`, 1)

	// Pure insertion (no deletions).
	checkUnifiedDiff(t, "file.txt", `
 line1
+inserted1
+inserted2
 line2
 line3
`, `
--- file.txt
+++ file.txt
@@ -1,3 +1,5 @@
 line1
+inserted1
+inserted2
 line2
 line3
`, 1)

	// Pure deletion (no insertions).
	checkUnifiedDiff(t, "file.txt", `
 line1
-deleted1
-deleted2
 line2
`, `
--- file.txt
+++ file.txt
@@ -1,4 +1,2 @@
 line1
-deleted1
-deleted2
 line2
`, 1)

	// Change at the very beginning of the file.
	checkUnifiedDiff(t, "file.txt", `
-old_first_line
+new_first_line
 line2
 line3
`, `
--- file.txt
+++ file.txt
@@ -1,3 +1,3 @@
-old_first_line
+new_first_line
 line2
 line3
`, 1)

	// Change at the very end of the file.
	checkUnifiedDiff(t, "file.txt", `
 line1
 line2
-old_last_line
+new_last_line
`, `
--- file.txt
+++ file.txt
@@ -2,2 +2,2 @@
 line2
-old_last_line
+new_last_line
`, 1)

	// Hunks that merge due to small gap (gap <= 2*context).
	checkUnifiedDiff(t, "file.txt", `
-line1
+changed1
 gap
-line3
+changed3
`, `
--- file.txt
+++ file.txt
@@ -1,3 +1,3 @@
-line1
+changed1
 gap
-line3
+changed3
`, 1)

	// Zero context.
	checkUnifiedDiff(t, "file.txt", `
 line1
 line2
-deleted
 line3
`, `
--- file.txt
+++ file.txt
@@ -3,1 +3,0 @@
-deleted
`, 0)

}
