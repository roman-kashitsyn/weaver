package main

// =================
// Cram test harness
// =================

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cramCommand struct {
	lineNumber int
	command    string
	input      []string
	output     []string
}

// The cram runner understands "  $ " commands, "  > " input lines, and
// expected output lines prefixed with two spaces.
func runCramFile(t *testing.T, file string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read cram test: %s", err)
	}
	commands, err := parseCram(data)
	if err != nil {
		t.Fatalf("parse cram test: %s", err)
	}

	dir := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %s", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %s", err)
	}
	defer func() {
		if err := os.Chdir(oldwd); err != nil {
			t.Fatalf("restore working directory: %s", err)
		}
	}()
	t.Setenv("SFVC_AUTHOR", "tester")
	t.Setenv("SFVC_DATE", "2001-02-03T04:05:06Z")

	for _, command := range commands {
		got, err := runCramCommand(command)
		if err != nil {
			t.Fatalf("%s:%d: $ %s: %s", file, command.lineNumber, command.command, err)
		}
		want := cramOutput(command.output)
		if got != want {
			t.Fatalf("%s:%d: $ %s\nwant:\n%sgot:\n%s", file, command.lineNumber, command.command, cramBlock(want), cramBlock(got))
		}
	}
}

func parseCram(data []byte) ([]cramCommand, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var commands []cramCommand
	for i := 0; i < len(lines); {
		line := lines[i]
		if !strings.HasPrefix(line, "  $ ") {
			i++
			continue
		}
		command := cramCommand{
			lineNumber: i + 1,
			command:    strings.TrimPrefix(line, "  $ "),
		}
		i++
		for i < len(lines) && strings.HasPrefix(lines[i], "  > ") {
			input, err := decodeCramEscapes(strings.TrimPrefix(lines[i], "  > "))
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, err)
			}
			command.input = append(command.input, input)
			i++
		}
		for i < len(lines) && !strings.HasPrefix(lines[i], "  $ ") {
			if after, ok := strings.CutPrefix(lines[i], "  "); ok {
				output, err := decodeCramEscapes(after)
				if err != nil {
					return nil, fmt.Errorf("line %d: %w", i+1, err)
				}
				command.output = append(command.output, output)
			}
			i++
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func runCramCommand(command cramCommand) (string, error) {
	args, err := splitCramFields(command.command)
	if err != nil {
		return "", err
	}
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "sfvc":
		var stdout strings.Builder
		if err := runCLI(args, &stdout); err != nil {
			fmt.Fprintf(&stdout, "sfvc: %s\n", err)
		}
		return stdout.String(), nil
	case "write":
		if len(args) != 2 {
			return "", fmt.Errorf("write expects FILE")
		}
		if err := os.MkdirAll(filepath.Dir(args[1]), 0o755); err != nil {
			return "", err
		}
		data := cramOutput(command.input)
		return "", os.WriteFile(args[1], []byte(data), 0o644)
	case "cat":
		if len(args) != 2 {
			return "", fmt.Errorf("cat expects FILE")
		}
		data, err := os.ReadFile(args[1])
		if err != nil {
			return "", err
		}
		return string(data), nil
	case "test":
		if len(args) != 3 || args[1] != "-f" {
			return "", fmt.Errorf("only test -f FILE is supported")
		}
		info, err := os.Stat(args[2])
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory", args[2])
		}
		return "", nil
	default:
		return "", fmt.Errorf("unsupported command %q", args[0])
	}
}

func splitCramFields(line string) ([]string, error) {
	var args []string
	var buf strings.Builder
	var quote byte
	inWord := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			switch c {
			case quote:
				quote = 0
			case '\\':
				if i+1 >= len(line) {
					buf.WriteByte(c)
				} else {
					i++
					writeCramEscape(&buf, line[i])
				}
			default:
				buf.WriteByte(c)
			}
			inWord = true
			continue
		}
		switch c {
		case ' ', '\t':
			if inWord {
				args = append(args, buf.String())
				buf.Reset()
				inWord = false
			}
		case '\'', '"':
			quote = c
			inWord = true
		case '\\':
			if i+1 >= len(line) {
				buf.WriteByte(c)
			} else {
				i++
				writeCramEscape(&buf, line[i])
			}
			inWord = true
		default:
			buf.WriteByte(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	if inWord {
		args = append(args, buf.String())
	}
	return args, nil
}

func decodeCramEscapes(line string) (string, error) {
	var buf strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] != '\\' {
			buf.WriteByte(line[i])
			continue
		}
		if i+1 >= len(line) {
			return "", fmt.Errorf("trailing backslash")
		}
		i++
		writeCramEscape(&buf, line[i])
	}
	return buf.String(), nil
}

func writeCramEscape(buf *strings.Builder, c byte) {
	switch c {
	case 'n':
		buf.WriteByte('\n')
	case 't':
		buf.WriteByte('\t')
	case '\\':
		buf.WriteByte('\\')
	default:
		buf.WriteByte('\\')
		buf.WriteByte(c)
	}
}

func cramOutput(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func cramBlock(s string) string {
	if s == "" {
		return "<no output>\n"
	}
	return s
}
