package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// watchFixture retains every observed line. Only validated periodic progress
// is omitted from the illustrated mutation transcript, never from raw evidence.
type watchFixture struct {
	epoch         string
	frontier      uint64
	json          bool
	raw, semantic []string
	eligible      []uint64
	schemas       int
}

func readWatchFixtureLine(lines <-chan string, deadline time.Time) (string, error) {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case line, open := <-lines:
		if !open {
			return "", io.EOF
		}
		return line, nil
	case <-timer.C:
		return "", fmt.Errorf("WATCH fixture deadline reached")
	}
}

func fixtureUint(raw json.RawMessage) (uint64, error) {
	text := string(raw)
	if strings.HasPrefix(text, `"`) {
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
	}
	if text == "" || strings.Trim(text, "0123456789") != "" {
		return 0, fmt.Errorf("invalid unsigned metadata %s", raw)
	}
	return strconv.ParseUint(text, 10, 64)
}

func fixtureJSON(line string) (map[string]json.RawMessage, string, watchPosition, error) {
	var event map[string]json.RawMessage
	var kind string
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return nil, "", watchPosition{}, err
	}
	if err := json.Unmarshal(event["type"], &kind); err != nil {
		return nil, "", watchPosition{}, err
	}
	var position struct {
		Epoch    string
		Revision json.RawMessage
	}
	if err := json.Unmarshal(event["position"], &position); err != nil {
		return nil, "", watchPosition{}, err
	}
	n, err := fixtureUint(position.Revision)
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(position.Epoch) {
		return nil, "", watchPosition{}, fmt.Errorf("invalid WATCH position: %s", line)
	}
	return event, kind, watchPosition{position.Epoch, n}, nil
}

func newWatchFixture(line string, jsonOutput bool) (*watchFixture, error) {
	f := &watchFixture{json: jsonOutput}
	var position watchPosition
	var err error
	if jsonOutput {
		_, kind, p, e := fixtureJSON(line)
		position, err = p, e
		if kind != "start" {
			return nil, fmt.Errorf("expected start: %s", line)
		}
	} else {
		if !strings.HasPrefix(line, "start ") {
			return nil, fmt.Errorf("expected start: %s", line)
		}
		position, err = parseWatchPosition(strings.TrimPrefix(line, "start "))
	}
	if err != nil {
		return nil, err
	}
	f.epoch, f.frontier = position.Epoch, position.Revision
	f.raw = append(f.raw, line)
	f.semantic = append(f.semantic, line)
	return f, nil
}

func fixtureStart(t *testing.T, p *watchProcess, jsonOutput bool) *watchFixture {
	t.Helper()
	f, err := newWatchFixture(p.line(t), jsonOutput)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func fixtureVersion(t *testing.T, text string) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(strings.TrimSpace(text), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// ancillary checks known progress/schema metadata against the current wait's
// bound. Schema does not advance delivered/ACK state; progress does.
func (f *watchFixture) ancillary(line string, progressCeiling, schemaCeiling, schemaVersion uint64) (bool, error) {
	var kind string
	var position watchPosition
	var version uint64
	if f.json {
		event, k, p, err := fixtureJSON(line)
		if err != nil {
			return false, err
		}
		kind, position = k, p
		if kind == "schema" {
			version, err = fixtureUint(event["version"])
			if err != nil {
				return false, err
			}
		}
	} else {
		words := strings.Fields(line)
		if len(words) == 0 {
			return false, fmt.Errorf("empty WATCH line")
		}
		kind = words[0]
		if kind != "progress" && kind != "schema" {
			return false, nil
		}
		if kind == "progress" && len(words) != 2 || kind == "schema" && (len(words) != 4 || words[2] != "version") {
			return false, fmt.Errorf("malformed %s: %s", kind, line)
		}
		var err error
		position, err = parseWatchPosition(words[1])
		if err != nil {
			return false, err
		}
		if kind == "schema" {
			version, err = strconv.ParseUint(words[3], 10, 64)
			if err != nil {
				return false, err
			}
		}
	}
	if kind != "progress" && kind != "schema" {
		return false, nil
	}
	if position.Epoch != f.epoch {
		return false, fmt.Errorf("wrong %s epoch: %s", kind, line)
	}
	if kind == "progress" {
		if position.Revision < f.frontier || position.Revision > progressCeiling {
			return false, fmt.Errorf("wrong progress frontier %d outside [%d,%d]", position.Revision, f.frontier, progressCeiling)
		}
		f.frontier = position.Revision
		f.eligible = append(f.eligible, position.Revision)
	} else {
		if position.Revision > schemaCeiling || version != schemaVersion {
			return false, fmt.Errorf("wrong schema position/version: %s", line)
		}
		f.schemas++
		f.semantic = append(f.semantic, line)
	}
	return true, nil
}

func (f *watchFixture) mutation(read func(time.Time) (string, error), kind string, revision, schemaVersion uint64) (string, string, error) {
	deadline := time.Now().Add(15 * time.Second)
	if revision == 0 {
		return "", "", fmt.Errorf("mutation revision must be positive")
	}
	for {
		line, err := read(deadline)
		if err != nil {
			return "", "", err
		}
		f.raw = append(f.raw, line)
		ancillary, err := f.ancillary(line, revision-1, revision, schemaVersion)
		if err != nil {
			return "", "", err
		}
		if ancillary {
			continue
		}
		var actual uint64
		if f.json {
			event, k, position, e := fixtureJSON(line)
			if e != nil {
				return "", "", e
			}
			if k != kind || position.Epoch != f.epoch {
				return "", "", fmt.Errorf("wrong mutation envelope: %s", line)
			}
			actual = position.Revision
			v, e := fixtureUint(event["schema_version"])
			if e != nil || v != schemaVersion {
				return "", "", fmt.Errorf("wrong mutation schema: %s", line)
			}
			if _, e := fixtureUint(event["commit_time_ms"]); e != nil {
				return "", "", e
			}
		} else {
			words := strings.Fields(line)
			if len(words) < 7 || words[0] != kind || words[1] != "revision" || words[3] != "time_ms" || words[5] != "user" {
				return "", "", fmt.Errorf("malformed mutation header: %s", line)
			}
			actual, err = strconv.ParseUint(words[2], 10, 64)
			if err != nil {
				return "", "", err
			}
			if _, err := strconv.ParseUint(words[4], 10, 64); err != nil {
				return "", "", err
			}
		}
		if actual != revision || actual < f.frontier {
			return "", "", fmt.Errorf("wrong mutation frontier %d; expected %d after %d", actual, revision, f.frontier)
		}
		var row string
		if !f.json {
			row, err = read(deadline)
			if err != nil {
				return "", "", err
			}
			f.raw = append(f.raw, row)
			if !strings.HasPrefix(row, "  block ") {
				return "", "", fmt.Errorf("missing complete block row: %s", row)
			}
		}
		f.semantic = append(f.semantic, line)
		if row != "" {
			f.semantic = append(f.semantic, row)
		}
		f.frontier = actual
		f.eligible = append(f.eligible, actual)
		return line, row, nil
	}
}

func (f *watchFixture) waitMutation(t *testing.T, p *watchProcess, kind string, revision, schemaVersion uint64) (string, string) {
	t.Helper()
	header, row, err := f.mutation(func(deadline time.Time) (string, error) { return readWatchFixtureLine(p.lines, deadline) }, kind, revision, schemaVersion)
	if err != nil {
		t.Fatalf("%v\nraw stream:\n%s", err, strings.Join(f.raw, "\n"))
	}
	return header, row
}

func (f *watchFixture) drain(line string, ceiling, schemaVersion uint64) error {
	f.raw = append(f.raw, line)
	known, err := f.ancillary(line, ceiling, ceiling, schemaVersion)
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("unexpected frame during final drain: %s", line)
	}
	return nil
}

// Drain while waiting for the owned process to exit so buffered progress can
// neither deadlock Wait nor be lost. The deadline is fixed, not per event.
func drainWatchFixture(lines <-chan string, done <-chan error, deadline time.Time, observe func(string) error, reaped ...func()) error {
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	var first error
	for lines != nil || done != nil {
		select {
		case line, open := <-lines:
			if !open {
				lines = nil
				continue
			}
			if observe != nil {
				if err := observe(line); err != nil && first == nil {
					first = err
				}
			}
		case err := <-done:
			for _, notify := range reaped {
				notify()
			}
			done = nil
			if err != nil && first == nil {
				first = err
			}
		case <-timer.C:
			return fmt.Errorf("WATCH drain deadline reached")
		}
	}
	return first
}

func (f *watchFixture) stop(t *testing.T, p *watchProcess, ceiling, schemaVersion uint64) {
	t.Helper()
	defer func() { t.Logf("Raw WATCH stream:\n%s", strings.Join(f.raw, "\n")) }()
	p.interrupt(t, func(line string) error { return f.drain(line, ceiling, schemaVersion) })
}

// The stream registers each eligible event before printing the next one.
// Cancellation can exclude only the final visible eligible event, including
// one discovered during drain. Every earlier event must still be represented
// by exact full-batch ACKs and the final partial flush.
func fixtureBatchACKs(wire string, eligible []uint64, batch uint64) error {
	if batch == 0 {
		return fmt.Errorf("invalid ACK batch")
	}
	actual, err := fixtureWireACKs(wire)
	if err != nil {
		return err
	}
	for i := 1; i < len(eligible); i++ {
		if eligible[i] < eligible[i-1] {
			return fmt.Errorf("regressed delivered frontier")
		}
	}
	minimum := len(eligible) - 1
	if minimum < 0 {
		minimum = 0
	}
	for registered := minimum; registered <= len(eligible); registered++ {
		var expected []uint64
		for i, revision := range eligible[:registered] {
			if uint64(i+1)%batch == 0 {
				expected = append(expected, revision)
			}
		}
		if registered > 0 && uint64(registered)%batch != 0 {
			expected = append(expected, eligible[registered-1])
		}
		if len(actual) != len(expected) {
			continue
		}
		equal := true
		for i := range actual {
			if actual[i] != expected[i] {
				equal = false
				break
			}
		}
		if equal {
			return nil
		}
	}
	return fmt.Errorf("premature/wrong ACK %v; delivered %v, only final visible event may be unregistered; batch %d", actual, eligible, batch)
}

func fixtureWireACKs(wire string) ([]uint64, error) {
	var revisions []uint64
	for _, match := range regexp.MustCompile(`(?m)^ACK ([0-9]+)\r\n`).FindAllStringSubmatch(wire, -1) {
		revision, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil {
			return nil, err
		}
		if len(revisions) > 0 && revision < revisions[len(revisions)-1] {
			return nil, fmt.Errorf("regressed wire ACK")
		}
		revisions = append(revisions, revision)
	}
	return revisions, nil
}

func fixtureSavedFrontier(t *testing.T, wire string, initial uint64) uint64 {
	t.Helper()
	acks, err := fixtureWireACKs(wire)
	if err != nil {
		t.Fatal(err)
	}
	if len(acks) == 0 {
		return initial
	}
	return acks[len(acks)-1]
}

// Captured complete wire ACK establishes registration before cancellation.
// Keep draining and validating output while waiting, under one fixed deadline.
func (f *watchFixture) awaitACK(t *testing.T, proxy *recordingProxy, lines <-chan string, revision, schemaVersion uint64) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		acknowledged, changed, err := proxy.acknowledged(revision)
		if err != nil {
			t.Fatal(err)
		}
		if acknowledged {
			return
		}
		select {
		case <-changed:
		case line, open := <-lines:
			if !open {
				t.Fatal("WATCH ended before registered ACK")
			}
			if err := f.drain(strings.TrimSuffix(line, "\n"), revision, schemaVersion); err != nil {
				t.Fatal(err)
			}
		case <-timer.C:
			t.Fatalf("WATCH did not send registered ACK %d; raw %v; wire %q", revision, f.raw, proxy.text())
		}
	}
}

func fixtureDocMutation(expected string) (uint64, uint64, error) {
	change := regexp.MustCompile(`(?m)^change revision ([0-9]+) time_ms `).FindStringSubmatch(expected)
	schema := regexp.MustCompile(`(?m)^schema <epoch>:[0-9]+ version ([0-9]+)$`).FindStringSubmatch(expected)
	if len(change) != 2 || len(schema) != 2 {
		return 0, 0, fmt.Errorf("marked WATCH requires exact change and schema metadata")
	}
	revision, err := strconv.ParseUint(change[1], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	version, err := strconv.ParseUint(schema[1], 10, 64)
	return revision, version, err
}

func fixtureTableFrontier(t *testing.T, s *testServer) uint64 {
	t.Helper()
	var schema map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s.ok(t, "--json", "DESCRIBE world")), &schema); err != nil {
		t.Fatal(err)
	}
	revision, err := fixtureUint(schema["revision"])
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func (f *watchFixture) waitSchema(t *testing.T, p *watchProcess, revision, version uint64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		line, err := readWatchFixtureLine(p.lines, deadline)
		if err != nil {
			t.Fatal(err)
		}
		f.raw = append(f.raw, line)
		before := f.schemas
		known, err := f.ancillary(line, revision, revision, version)
		if err != nil || !known {
			t.Fatalf("schema wait: %v; unexpected %s; raw %v", err, line, f.raw)
		}
		if f.schemas != before {
			return
		}
	}
}

func (f *watchFixture) waitResync(t *testing.T, p *watchProcess, epoch string, revision uint64) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		line, err := readWatchFixtureLine(p.lines, deadline)
		if err != nil {
			t.Fatal(err)
		}
		f.raw = append(f.raw, line)
		if strings.HasPrefix(line, "progress ") {
			if _, err := f.ancillary(line, revision, revision, 2); err != nil {
				t.Fatal(err)
			}
			continue
		}
		words := strings.Fields(line)
		if len(words) < 2 || words[0] != "resync" {
			t.Fatalf("unexpected resync: %s", line)
		}
		if err := f.resync(line, epoch, revision); err != nil {
			t.Fatal(err)
		}
		return line
	}
}

// A stale AFTER start identifies the requested history. RESYNC must identify
// the independently observed current table, with its exact committed frontier.
func (f *watchFixture) resync(line, epoch string, revision uint64) error {
	words := strings.Fields(line)
	if len(words) < 2 || words[0] != "resync" {
		return fmt.Errorf("unexpected resync: %s", line)
	}
	position, err := parseWatchPosition(strings.TrimSuffix(words[1], ";"))
	if err != nil || position.Epoch != epoch || position.Revision != revision {
		return fmt.Errorf("wrong resync position: %s %v", line, err)
	}
	f.epoch, f.frontier = position.Epoch, position.Revision
	f.semantic = append(f.semantic, line)
	return nil
}

// Expected terminal failures still drain and validate all periodic output.
func (f *watchFixture) expectFailure(t *testing.T, p *watchProcess, ceiling uint64, message string) {
	t.Helper()
	var validation error
	err := drainWatchFixture(p.lines, p.done, time.Now().Add(8*time.Second), func(line string) error {
		f.raw = append(f.raw, line)
		if !strings.HasPrefix(line, "progress ") {
			validation = fmt.Errorf("unexpected output before terminal failure: %s", line)
			return validation
		}
		_, err := f.ancillary(line, ceiling, ceiling, 1)
		if err != nil {
			validation = err
		}
		return err
	}, func() { p.finished = true })
	t.Logf("Raw terminal WATCH stream:\n%s", strings.Join(f.raw, "\n"))
	if !p.finished {
		t.Fatalf("WATCH terminal deadline before reaping child: %v", err)
	}
	if validation != nil || err == nil || !strings.Contains(p.stderr.String(), message) {
		t.Fatalf("WATCH terminal error: %v; validation %v; stderr %q", err, validation, p.stderr.String())
	}
}

func fixtureVersionFromStart(t *testing.T, line string, jsonOutput bool) uint64 {
	t.Helper()
	trace, err := newWatchFixture(line, jsonOutput)
	if err != nil {
		t.Fatal(err)
	}
	return trace.frontier
}
