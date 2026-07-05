package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
)

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	code, err := run(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	goArgs := append([]string{"test", "-json"}, args...)
	cmd := exec.Command("go", goArgs...)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 1, fmt.Errorf("noskipgotest: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return 1, fmt.Errorf("noskipgotest: start go test: %w", err)
	}

	result := scanResult{}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		result.observe(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		_ = cmd.Wait()
		return 1, fmt.Errorf("noskipgotest: read go test output: %w", err)
	}
	waitErr := cmd.Wait()
	if waitErr != nil {
		return 1, nil
	}
	if result.testEvents == 0 {
		return 1, fmt.Errorf("noskipgotest: no tests were executed")
	}
	if result.skips > 0 {
		return 1, fmt.Errorf("noskipgotest: %d test(s) skipped; acceptance is incomplete", result.skips)
	}
	return 0, nil
}

type scanResult struct {
	testEvents int
	skips      int
}

func (r *scanResult) observe(line []byte) {
	var ev testEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		fmt.Println(string(line))
		return
	}
	if ev.Output != "" {
		fmt.Print(ev.Output)
	}
	if ev.Test == "" {
		return
	}
	switch ev.Action {
	case "run":
		r.testEvents++
	case "skip":
		r.skips++
	}
}
