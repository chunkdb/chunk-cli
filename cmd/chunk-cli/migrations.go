package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

type migrationStep struct {
	name, statement string
}

func checkMigrationOptions(opts statementOptions) error {
	if opts.blocks || opts.in != "" || opts.out != "" || opts.newPasswordFile != "" {
		return errors.New("migrate and migrations take only connection options and --json")
	}
	return nil
}

func validMigrationName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c != '_' && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func readMigrationFile(path string) ([]migrationStep, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read migrations %s: %w", path, err)
	}
	defer file.Close()
	steps, err := parseMigrations(file)
	if err != nil {
		return nil, fmt.Errorf("read migrations %s: %w", path, err)
	}
	return steps, nil
}

// parseMigrations joins trimmed statement lines with one space. Quotes cannot
// span lines, so joining never changes the contents of a CQL literal.
func parseMigrations(r io.Reader) ([]migrationStep, error) {
	steps := make([]migrationStep, 0)
	seen := make(map[string]bool)
	current := migrationStep{}
	finish := func() error {
		if current.name == "" {
			return nil
		}
		if current.statement == "" {
			return fmt.Errorf("migration %q has no statement", current.name)
		}
		words := strings.Fields(current.statement)
		allowed := len(words) >= 2 && ((strings.EqualFold(words[0], "CREATE") || strings.EqualFold(words[0], "DROP")) &&
			(strings.EqualFold(words[1], "TABLE") || strings.EqualFold(words[1], "SLOT")) ||
			strings.EqualFold(words[0], "ALTER") && strings.EqualFold(words[1], "TABLE") ||
			strings.EqualFold(words[0], "GRANT") || strings.EqualFold(words[0], "REVOKE"))
		if !allowed {
			return fmt.Errorf("migration %q needs CREATE/ALTER/DROP TABLE, GRANT/REVOKE or CREATE/DROP SLOT", current.name)
		}
		steps = append(steps, current)
		return nil
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 65537)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if strings.ContainsAny(line, "\x00\r\n") {
			return nil, fmt.Errorf("line %d contains NUL, CR or LF", lineNumber)
		}
		if strings.HasPrefix(line, "-- migrate:") {
			if err := finish(); err != nil {
				return nil, err
			}
			name := strings.TrimSpace(strings.TrimPrefix(line, "-- migrate:"))
			if !validMigrationName(name) {
				return nil, fmt.Errorf("line %d: migration name must match [a-z_][a-z0-9_]*, 1–63 bytes", lineNumber)
			}
			if seen[name] {
				return nil, fmt.Errorf("line %d: duplicate migration %q", lineNumber, name)
			}
			seen[name] = true
			current = migrationStep{name: name}
			continue
		}
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if current.name == "" {
			return nil, fmt.Errorf("line %d: statement needs a preceding -- migrate: <name>", lineNumber)
		}
		quoted := false
		for i := range len(line) {
			switch line[i] {
			case '\'':
				quoted = !quoted
			case ';', '$':
				if !quoted {
					return nil, fmt.Errorf("migration %q: semicolon separators and parameters are not supported", current.name)
				}
			}
		}
		if quoted {
			return nil, fmt.Errorf("migration %q: quoted values must fit on one line", current.name)
		}
		if current.statement != "" {
			current.statement += " "
		}
		current.statement += line
		if len(current.statement) > 65536 {
			return nil, fmt.Errorf("migration %q exceeds 65536 statement bytes", current.name)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read line %d: %w", lineNumber+1, err)
	}
	if err := finish(); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, errors.New("migration file has no steps")
	}
	return steps, nil
}

func runMigrations(client *chunkclient.Client, steps []migrationStep, stdout io.Writer, json bool) error {
	for _, step := range steps {
		reply, err := client.Do("MIGRATE '" + step.name + "' " + step.statement)
		if err != nil {
			return fmt.Errorf("migration %q: %w", step.name, err)
		}
		if reply.Kind != chunkclient.KindSimple || (reply.Text != "applied" && reply.Text != "skipped") {
			return fmt.Errorf("migration %q: expected applied or skipped simple string", step.name)
		}
		if json {
			err = writeJSON(stdout, orderedMap{{"name", step.name}, {"status", reply.Text}})
		} else {
			_, err = fmt.Fprintf(stdout, "%s %s\n", step.name, reply.Text)
		}
		if err != nil {
			return fmt.Errorf("print migration %q: %w", step.name, err)
		}
	}
	return nil
}

func listMigrations(client *chunkclient.Client, stdout io.Writer, opts statementOptions) error {
	reply, err := client.Do("SHOW MIGRATIONS")
	if err != nil {
		return err
	}
	if reply.Kind != chunkclient.KindArray {
		return errors.New("SHOW MIGRATIONS: expected an array of records")
	}
	for _, record := range reply.Items {
		if record.Kind != chunkclient.KindMap {
			return errors.New("SHOW MIGRATIONS: expected a record map")
		}
		for _, key := range []string{"name", "user", "statement"} {
			value, ok := record.Lookup(key)
			if !ok || value.Kind != chunkclient.KindBulk {
				return fmt.Errorf("SHOW MIGRATIONS: expected bulk string %s", key)
			}
		}
		applied, ok := record.Lookup("applied_ms")
		if _, err := applied.Int64(); !ok || err != nil {
			return errors.New("SHOW MIGRATIONS: expected integer applied_ms")
		}
	}
	return printReply(stdout, reply, opts)
}
