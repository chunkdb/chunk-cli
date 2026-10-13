// Command testresults checks a `go test -json` log in CI: no test may be
// skipped, and every required integration test must have passed.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

type event struct {
	Action string
	Test   string
}

func check(path string, required []string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	passed := map[string]bool{}
	var skipped []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		var e event
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return 0, fmt.Errorf("decode %s: %w", path, err)
		}
		if e.Test == "" {
			continue
		}
		switch e.Action {
		case "skip":
			skipped = append(skipped, e.Test)
		case "pass":
			passed[e.Test] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	var missing []string
	for _, name := range required {
		if !passed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(skipped) > 0 || len(missing) > 0 {
		return 0, fmt.Errorf("skipped tests: %v; missing integration results: %v", skipped, missing)
	}
	return len(passed), nil
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: testresults <go-test.jsonl> <RequiredTest,...>")
		os.Exit(2)
	}
	count, err := check(os.Args[1], strings.Split(os.Args[2], ","))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Passed %d test cases with no skipped tests\n", count)
}
