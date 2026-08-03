package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type mutation struct {
	from string
	to   string
}

var operators = []mutation{
	{"==", "!="},
	{"!=", "=="},
	{"<=", ">="},
	{">=", "<="},
	{"<", ">"},
	{">", "<"},
	{"&&", "||"},
	{"||", "&&"},
}

var literals = []mutation{
	{"true", "false"},
	{"false", "true"},
}

// candidates returns the mutations that apply to a single line. Guards skip
// operators that are substrings of a longer operator already covered (e.g.
// "<" when the line has "<=") to avoid nonsense mutations.
func candidates(line string) []mutation {
	var out []mutation
	for _, m := range operators {
		if !strings.Contains(line, m.from) {
			continue
		}
		switch m.from {
		case "<":
			if strings.Contains(line, "<=") || strings.Contains(line, "<<") || strings.Contains(line, "<-") {
				continue
			}
		case ">":
			if strings.Contains(line, ">=") || strings.Contains(line, ">>") {
				continue
			}
		}
		out = append(out, m)
	}
	for _, m := range literals {
		if wordIn(line, m.from) {
			out = append(out, m)
		}
	}
	return out
}

func wordIn(line, word string) bool {
	for _, part := range strings.Fields(line) {
		trimmed := strings.Trim(part, "\"()[],;")
		if trimmed == word {
			return true
		}
	}
	return false
}

func apply(line string, m mutation) string {
	return strings.Replace(line, m.from, m.to, 1)
}

func main() {
	max := flag.Int("max", 0, "max mutations to run (0 = all)")
	testCmd := flag.String("test", "go test ./internal/... ./testing/...", "test command to run in the repo")
	fileFlag := flag.String("file", "", "source file to mutate (required)")
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 || *fileFlag == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./tools/mutate.go <repo-path> --file <file.go> [--max N] [--test CMD]")
		os.Exit(2)
	}
	repo, file := args[0], filepath.Join(args[0], *fileFlag)

	original, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read file:", err)
		os.Exit(1)
	}
	lines := strings.Split(string(original), "\n")

	type job struct {
		line int
		m    mutation
	}
	var jobs []job
	for i, line := range lines {
		for _, m := range candidates(line) {
			jobs = append(jobs, job{i, m})
		}
	}
	if len(jobs) == 0 {
		fmt.Println("total=0 killed=0 survived=0 score=100.0%")
		return
	}

	seen := map[string]bool{}
	uniq := jobs[:0]
	for _, j := range jobs {
		key := fmt.Sprintf("%d:%s->%s", j.line, j.m.from, j.m.to)
		if !seen[key] {
			seen[key] = true
			uniq = append(uniq, j)
		}
	}
	jobs = uniq
	if *max > 0 && len(jobs) > *max {
		jobs = jobs[:*max]
	}

	killed, survived := 0, 0
	var survivors []string
	for _, j := range jobs {
		origLine := lines[j.line]
		lines[j.line] = apply(origLine, j.m)
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		ok := runTests(repo, *testCmd)
		lines[j.line] = origLine
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		if !ok {
			killed++
		} else {
			survived++
			survivors = append(survivors, fmt.Sprintf("%s:%d  %q -> %q", file, j.line+1, j.m.from, j.m.to))
		}
	}

	total := killed + survived
	score := 100.0
	if total > 0 {
		score = float64(killed) / float64(total) * 100.0
	}
	fmt.Printf("total=%d killed=%d survived=%d score=%.1f%%\n", total, killed, survived, score)
	for _, s := range survivors {
		fmt.Println("SURVIVED " + s)
	}
}

func runTests(repo, cmd string) bool {
	c := exec.Command("sh", "-c", cmd)
	c.Dir = repo
	c.Env = os.Environ()
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	return c.Run() == nil
}
