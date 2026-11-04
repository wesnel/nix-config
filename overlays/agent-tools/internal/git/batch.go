package git

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// BatchCommits visits requested full object IDs in order. run executes Git
// with the caller's own environment and protections. raw belongs to the
// current batch and must not be retained by visit. Each batch contains at
// most one MiB of commit data, except that one larger object travels alone.
func BatchCommits(ids []string, run func([]byte, ...string) ([]byte, error), visit func(string, []byte) error) error {
	if len(ids) == 0 {
		return nil
	}
	var input strings.Builder
	for _, id := range ids {
		if len(id) != 40 && len(id) != 64 {
			return fmt.Errorf("invalid commit object ID")
		}
		if _, err := hex.DecodeString(id); err != nil || strings.ToLower(id) != id {
			return fmt.Errorf("invalid commit object ID")
		}
		input.WriteString(id + "\n")
	}
	checked, err := run([]byte(input.String()), "cat-file", "--batch-check")
	if err != nil {
		return err
	}
	if !bytes.HasSuffix(checked, []byte("\n")) {
		return fmt.Errorf("git returned an incomplete commit list")
	}
	lines := strings.Split(strings.TrimSuffix(string(checked), "\n"), "\n")
	if len(lines) != len(ids) {
		return fmt.Errorf("git returned an incomplete commit list")
	}
	sizes := make([]int, len(ids))
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != ids[i] || fields[1] != "commit" {
			return fmt.Errorf("git returned an invalid commit description")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return fmt.Errorf("git returned an invalid commit size")
		}
		sizes[i] = size
	}
	const maxBatch = 1 << 20
	for start := 0; start < len(ids); {
		end, total := start, 0
		input.Reset()
		for end < len(ids) && (end == start || sizes[end] <= maxBatch-total) {
			input.WriteString(ids[end] + "\n")
			total += sizes[end]
			end++
			if total >= maxBatch {
				break
			}
		}
		out, err := run([]byte(input.String()), "cat-file", "--batch")
		if err != nil {
			return err
		}
		for i := start; i < end; i++ {
			line, rest, ok := bytes.Cut(out, []byte("\n"))
			fields := strings.Fields(string(line))
			if !ok || len(fields) != 3 || fields[0] != ids[i] || fields[1] != "commit" || fields[2] != strconv.Itoa(sizes[i]) || sizes[i] >= len(rest) || rest[sizes[i]] != '\n' {
				return fmt.Errorf("git returned an invalid commit object")
			}
			if err := visit(ids[i], rest[:sizes[i]]); err != nil {
				return err
			}
			out = rest[sizes[i]+1:]
		}
		if len(out) != 0 {
			return fmt.Errorf("git returned extra commit data")
		}
		start = end
	}
	return nil
}
