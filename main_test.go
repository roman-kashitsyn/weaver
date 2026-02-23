package main

import (
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
