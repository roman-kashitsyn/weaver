package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func check(t *testing.T, a, b []int, want string) {
	t.Helper()
	got := FormatScript(DiffScript(a, b))
	if got != want {
		t.Errorf("DiffScript(%v, %v):\ngot:\n%s\nwant:\n%s", a, b, got, want)
	}
}

func TestDiffScript(t *testing.T) {
	check(t, []int{1, 2, 3}, []int{1, 2, 3}, ` 1
 2
 3
`)
	check(t, []int{}, []int{}, ``)
	check(t, []int{}, []int{1, 2, 3}, `+1
+2
+3
`)
	check(t, []int{1, 2, 3}, []int{}, `-1
-2
-3
`)
	check(t, []int{2, 3}, []int{1, 2, 3}, `+1
 2
 3
`)
	check(t, []int{1, 2}, []int{1, 2, 3}, ` 1
 2
+3
`)
	check(t, []int{1, 2, 3}, []int{2, 3}, `-1
 2
 3
`)
	check(t, []int{1, 2, 3}, []int{1, 2}, ` 1
 2
-3
`)
	check(t, []int{1, 2, 3}, []int{1, 5, 3}, ` 1
-2
+5
 3
`)
	check(t, []int{1, 2}, []int{3, 4}, `-1
-2
+3
+4
`)
	check(t, []int{1, 3, 5}, []int{1, 2, 3, 4, 5}, ` 1
+2
 3
+4
 5
`)
	check(t, []int{1}, []int{1}, ` 1
`)
	check(t, []int{1}, []int{2}, `-1
+2
`)
	check(t, []int{4, 1, 8, 8, 8, 1, 2, 3, 4}, []int{4, 9, 8, 1, 2, 3, 5}, ` 4
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
				instr.Type = InstrBeginAdd
			} else if cmd == "^AD" {
				instr.Type = InstrBeginDel
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
	mask, err := Reconstruct(instructions, VersionGraph{}, v)
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
