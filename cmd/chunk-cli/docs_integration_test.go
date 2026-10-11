package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

type docExample struct {
	kind, input, expected string
}

var docFence = regexp.MustCompile("(?ms)^```([^\\n]*)\\n(.*?)\\n```")

func parseDocExamples(source string) ([]docExample, error) {
	fences := docFence.FindAllStringSubmatchIndex(source, -1)
	var examples []docExample
	for i, fence := range fences {
		tag := source[fence[2]:fence[3]]
		if !strings.Contains(tag, "docs-check") {
			continue
		}
		if tag != "sh docs-check-shell" && tag != "sh docs-check-watch" && tag != "cql docs-check" && tag != "sh docs-check-error" {
			return nil, fmt.Errorf("unknown example marker %q", tag)
		}
		if i+1 == len(fences) {
			return nil, fmt.Errorf("missing expected output")
		}
		next := fences[i+1]
		if source[next[2]:next[3]] != "output" || strings.TrimSpace(source[fence[1]:next[0]]) != "" {
			return nil, fmt.Errorf("checked input must be followed immediately by an output fence")
		}
		examples = append(examples, docExample{tag, source[fence[4]:fence[5]], source[next[4]:next[5]]})
	}
	return examples, nil
}

// Replace only explicitly volatile metadata. Data values, revisions, chunk
// versions, counts, and every other byte remain part of the comparison.
func normalizeDocOutput(output string) string {
	for _, rule := range []struct{ pattern, replacement string }{
		{`(?m)^((?:start |schema ))[0-9a-f]{32}(:)`, `${1}<epoch>${2}`},
		{`(?m)^([ \t]*\d+\) table = [a-z][a-z0-9_]*(?:, name = [a-z_][a-z0-9_]*)?, epoch = )[0-9a-f]{32}(,)`, `${1}<epoch>${2}`},
		{`(?m)^([1-9]\d*\)\n  table = [a-z_][a-z0-9_]*\n  name = [a-z_][a-z0-9_]*\n  epoch = )[0-9a-f]{32}(\n  acked = \d+\n  retained_bytes = \d+\n  lost = (?:true|false)\n  filters:\n)`, `${1}<epoch>${2}`},
		{`(?m)^(change revision \d+ time_ms )-?\d+`, `${1}<time-ms>`},
		{`(?m)^(\d+\) name = [a-z_][a-z0-9_]*, applied_ms = )\d+`, `${1}<time-ms>`},
	} {
		output = regexp.MustCompile(rule.pattern).ReplaceAllString(output, rule.replacement)
	}
	return strings.TrimRight(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
}

func TestDocExampleMarkers(t *testing.T) {
	for _, source := range []string{
		"```sh docs-check-shell\ntrue\n```",
		"```sh docs-check-shell\ntrue\n```\nexplanation\n```output\nOK\n```",
		"```sh docs-check-shell\ntrue\n```\n```text\nOK\n```",
		"```sh docs-check-unknown\ntrue\n```\n```output\nOK\n```",
	} {
		if _, err := parseDocExamples(source); err == nil {
			t.Fatalf("accepted malformed example: %s", source)
		}
	}
	source := "```sh docs-check-shell\nchunk-cli PING\n```\n\n```output\nPONG\n```"
	examples, err := parseDocExamples(source)
	if err != nil || len(examples) != 1 || examples[0].input != "chunk-cli PING" || examples[0].expected != "PONG" {
		t.Fatalf("parse: %#v, %v", examples, err)
	}
}

func TestDocOutputNormalization(t *testing.T) {
	epoch := "0123456789abcdef0123456789abcdef"
	source := "start " + epoch + ":2\nchange revision 3 time_ms 123 user admin\n1) name = step, applied_ms = 456, user = admin\n"
	want := "start <epoch>:2\nchange revision 3 time_ms <time-ms> user admin\n1) name = step, applied_ms = <time-ms>, user = admin"
	if got := normalizeDocOutput(source); got != want {
		t.Fatalf("normalized: %q; want %q", got, want)
	}
	data := "kind = 42\nname = '" + epoch + "'\nname = 'time_ms 123'\nversion = 2\n"
	if got := normalizeDocOutput(data); got != strings.TrimSuffix(data, "\n") {
		t.Fatalf("changed data values: %q", got)
	}
}

func TestDocSlotTranscript(t *testing.T) {
	// Use the production formatter, including the nested filters map. The
	// docs fence must keep every field and value rather than flattening it.
	null := chunkclient.Value{Kind: chunkclient.KindNull}
	filters := foundationMapValue(
		watchBulk("area"), watchArray(watchInt("0"), watchInt("0"), watchInt("0"), watchInt("0")),
		watchBulk("columns"), null, watchBulk("column_ids"), null,
		watchBulk("no_before"), chunkclient.Value{Kind: chunkclient.KindBool},
		watchBulk("chunks"), chunkclient.Value{Kind: chunkclient.KindBool},
	)
	slot := foundationMapValue(
		watchBulk("table"), watchBulk("world"), watchBulk("name"), watchBulk("consumer"),
		watchBulk("epoch"), watchBulk(watchEpoch), watchBulk("acked"), watchInt("3"),
		watchBulk("retained_bytes"), watchInt("0"), watchBulk("lost"), chunkclient.Value{Kind: chunkclient.KindBool},
		watchBulk("filters"), filters, watchBulk("lost_reason"), null,
	)
	var out bytes.Buffer
	if err := printReply(&out, watchArray(slot), statementOptions{}); err != nil {
		t.Fatal(err)
	}
	out.WriteString("OK\n")
	source, err := os.ReadFile(filepath.Join("..", "..", "docs", "FEED.md"))
	if err != nil {
		t.Fatal(err)
	}
	examples, err := parseDocExamples(string(source))
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	for _, example := range examples {
		if strings.Contains(example.input, "SHOW SLOTS ON world") {
			expected = example.expected
		}
	}
	if expected == "" || normalizeDocOutput(out.String()) != expected {
		t.Fatalf("formatted SHOW SLOTS:\n%s\nexpected marked transcript:\n%s", normalizeDocOutput(out.String()), expected)
	}
	t.Logf("Synthetic production formatter transcript:\n%s", out.String())
	for _, replacement := range [][2]string{
		{"acked = 3", "acked = 4"}, {"retained_bytes = 0", "retained_bytes = 7"},
		{"lost = false", "lost = true"}, {"area = [0, 0, 0, 0]", "area = [0, 0, 1, 1]"},
		{"columns = NULL", "columns = [kind]"}, {"column_ids = NULL", "column_ids = [1]"},
		{"no_before = false", "no_before = true"}, {"chunks = false", "chunks = true"},
		{"lost_reason = NULL", "lost_reason = schema_filter"},
	} {
		changed := strings.Replace(out.String(), replacement[0], replacement[1], 1)
		if normalizeDocOutput(changed) == expected {
			t.Fatalf("normalization hid changed metadata: %v", replacement)
		}
	}
	for _, data := range []string{
		"epoch = " + watchEpoch + "\n",
		"  epoch = " + watchEpoch + "\n",
		"value = 'epoch = " + watchEpoch + "'\n",
		"1)\n  table = world\n  name = consumer\n  epoch = " + watchEpoch + "\n  value = 3\n",
		"1)\n  table = world\n  name = consumer\n  epoch = " + watchEpoch + " extra\n  acked = 3\n  retained_bytes = 0\n  lost = false\n  filters:\n",
	} {
		if got := normalizeDocOutput(data); got != strings.TrimSuffix(data, "\n") {
			t.Fatalf("normalization changed epoch-like data: %q -> %q", data, got)
		}
	}
}

type docWatch struct {
	command      *exec.Cmd
	lines        chan string
	done         chan error
	stderr       bytes.Buffer
	stdout       io.ReadCloser
	cancel       context.CancelFunc
	backpressure chan struct{}
	finished     bool
}

func (w *docWatch) line(t *testing.T, label string) string {
	t.Helper()
	select {
	case line, ok := <-w.lines:
		if !ok {
			// Wait publishes stderr and the exit status before reading them.
			err := <-w.done
			w.finished = true
			t.Fatalf("docs watch ended waiting for %s: %v; %s", label, err, w.stderr.String())
		}
		return line + "\n"
	case <-time.After(15 * time.Second):
		t.Fatalf("docs watch timed out waiting for %s", label)
		return ""
	}
}

func startDocWatch(t *testing.T, code, directory string, env []string) *docWatch {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &docWatch{lines: make(chan string, 16), done: make(chan error, 1), cancel: cancel,
		backpressure: make(chan struct{}, 1)}
	w.command = exec.Command("bash", "-eu", "-o", "pipefail", "-c", "exec "+code)
	w.command.Dir, w.command.Env, w.command.Stderr = directory, env, &w.stderr
	stdout, err := w.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	w.stdout = stdout
	if err := w.command.Start(); err != nil {
		cancel()
		_ = stdout.Close()
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
	scan:
		for scanner.Scan() {
			// Normal reads are lossless. Once a failed consumer cancels,
			// neither a full output channel nor a blocked pipe read can
			// prevent the owned child from being reaped.
			select {
			case w.lines <- scanner.Text():
				continue
			default:
			}
			select {
			case w.backpressure <- struct{}{}:
			default:
			}
			select {
			case w.lines <- scanner.Text():
			case <-ctx.Done():
				break scan
			}
		}
		err := scanner.Err()
		if waitErr := w.command.Wait(); err == nil {
			err = waitErr
		}
		close(w.lines)
		w.done <- err
	}()
	t.Cleanup(func() {
		if err := w.cleanup(); err != nil {
			t.Error(err)
		}
	})
	return w
}

// cleanup abandons output only after the test stops consuming it. Closing
// the pipe interrupts Scan; cancelling the channel send interrupts backpressure.
func (w *docWatch) cleanup() error {
	w.cancel()
	if w.finished {
		return nil
	}
	_ = w.stdout.Close()
	_ = w.command.Process.Signal(os.Interrupt)
	select {
	case <-w.done:
		w.finished = true
		return nil
	case <-time.After(5 * time.Second):
		_ = w.command.Process.Kill()
	}
	select {
	case <-w.done:
		w.finished = true
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("docs watch cleanup timed out waiting to reap killed child")
	}
}

func TestDocWatchCleanupAfterBackpressure(t *testing.T) {
	code := `bash -c 'for ((i=0; i<40; i++)); do printf "unexpected %d\n" "$i"; done'`
	t.Run("abandoned_output", func(t *testing.T) {
		w := startDocWatch(t, code, t.TempDir(), os.Environ())
		// A real full-channel signal reproduces the failed-consumer path.
		// No more lines are consumed after this point.
		if line := w.line(t, "first unexpected line"); line != "unexpected 0\n" {
			t.Fatalf("unexpected synthetic output: %q", line)
		}
		select {
		case <-w.backpressure:
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic watch did not fill its output channel")
		}
		if err := w.cleanup(); err != nil {
			t.Fatal(err)
		}
		if !w.finished || w.command.ProcessState == nil {
			t.Fatal("cleanup returned before reaping the synthetic watch")
		}
	})
	t.Run("complete_output", func(t *testing.T) {
		w := startDocWatch(t, code, t.TempDir(), os.Environ())
		for i := 0; i < 40; i++ {
			if got, want := w.line(t, "complete synthetic transcript"), fmt.Sprintf("unexpected %d\n", i); got != want {
				t.Fatalf("synthetic transcript: got %q; want %q", got, want)
			}
		}
		select {
		case err := <-w.done:
			w.finished = true
			if err != nil {
				t.Fatalf("synthetic watch exit: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("synthetic watch did not exit after its complete transcript")
		}
	})
}

func (w *docWatch) finish(t *testing.T) {
	t.Helper()
	if err := w.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-w.done:
		w.finished = true
		if err != nil || w.stderr.Len() != 0 {
			t.Fatalf("docs watch shutdown: %v; %s", err, w.stderr.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("docs watch timed out waiting for UNWATCH shutdown")
	}
}

func TestCLIDocExamples(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cliDirectory := t.TempDir()
	binary := filepath.Join(cliDirectory, "chunk-cli")
	build := exec.Command("go", "build", "-o", binary, "./cmd/chunk-cli")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build docs CLI: %v\n%s", err, out)
	}
	pages, err := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	pages = append([]string{filepath.Join(root, "README.md")}, pages...)
	for _, page := range pages {
		t.Run(filepath.Base(page), func(t *testing.T) {
			source, err := os.ReadFile(page)
			if err != nil {
				t.Fatal(err)
			}
			examples, err := parseDocExamples(string(source))
			if err != nil || len(examples) == 0 {
				t.Fatalf("docs examples: %v; found %d", err, len(examples))
			}
			work := t.TempDir()
			backups, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			s := startServerConfigured(t, false, true, 2, "--backup-dir", backups)
			env := append(os.Environ(), "PATH="+cliDirectory+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CHUNKDB_URI="+s.uriFor(adminUser, ""), "CHUNKDB_PASSWORD="+adminPassword)
			var watch *docWatch
			watchIndex := -1
			outputs := make([]string, len(examples))
			for i, example := range examples {
				if example.kind == "sh docs-check-watch" {
					if watch != nil {
						t.Fatal("docs watch requires a following writer example")
					}
					watch = startDocWatch(t, example.input, work, env)
					outputs[i] = watch.line(t, "WATCH start")
					if !strings.HasPrefix(outputs[i], "start ") {
						t.Fatalf("unexpected WATCH start: %s", outputs[i])
					}
					watchIndex = i
					continue
				}
				if example.kind == "cql docs-check" {
					code, out, errout := s.cli(t, example.input+"\nexit\n", "shell")
					if code != 0 || errout != "" {
						t.Fatalf("example %d shell: exit %d; stderr %q", i+1, code, errout)
					}
					// CQL fences show replies without the shell's UI prompts.
					out = strings.TrimSuffix(out, "chunk> ")
					out = regexp.MustCompile(`(?m)^chunk\*?> `).ReplaceAllString(out, "")
					outputs[i] = out
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				command := exec.CommandContext(ctx, "bash", "-eu", "-o", "pipefail", "-c", example.input)
				command.Dir, command.Env = work, env
				var stdout, stderr bytes.Buffer
				command.Stdout, command.Stderr = &stdout, &stderr
				err := command.Run()
				cancel()
				if example.kind == "sh docs-check-error" {
					exit, ok := err.(*exec.ExitError)
					if !ok || exit.ExitCode() != 1 || stdout.Len() != 0 || stderr.Len() == 0 {
						t.Fatalf("example %d expected exit 1 and stderr: %v; stderr %q; stdout %q", i+1, err, stderr.String(), stdout.String())
					}
					outputs[i] = stderr.String()
				} else {
					if err != nil || stderr.Len() != 0 {
						t.Fatalf("example %d: %v; stderr %q; stdout %q", i+1, err, stderr.String(), stdout.String())
					}
					outputs[i] = stdout.String()
				}
				if watch != nil {
					// This checked transcript has one changed block. Its complete
					// row is the explicit signal before cancellation and slot inspection.
					change := watch.line(t, "schema or change header")
					if strings.HasPrefix(change, "schema ") {
						outputs[watchIndex] += change
						change = watch.line(t, "change header after schema")
					}
					row := watch.line(t, "complete changed block")
					if !strings.HasPrefix(change, "change revision ") || !strings.HasPrefix(row, "  block ") {
						t.Fatalf("unexpected change: %s%s", change, row)
					}
					outputs[watchIndex] += change + row
					watch.finish(t)
					for line := range watch.lines {
						outputs[watchIndex] += line + "\n"
					}
					watch = nil
				}
			}
			if watch != nil {
				t.Fatal("docs watch has no following writer example")
			}
			for i, example := range examples {
				if actual := normalizeDocOutput(outputs[i]); actual != example.expected {
					t.Errorf("example %d output differs\nexpected:\n%s\nactual:\n%s", i+1, example.expected, actual)
				}
			}
			t.Logf("checked %d Markdown examples", len(examples))
		})
	}
}
