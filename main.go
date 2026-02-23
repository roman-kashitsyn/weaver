package main

import (
	"fmt"
	"strings"
)

type Action int

const (
	Add Action = iota
	Delete
	Keep
)

type Delta struct {
	Action Action
	Items  []int
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
		case Delete:
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
			delta.Action = Delete
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
