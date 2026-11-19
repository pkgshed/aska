package vcs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	// ErrPathNotFound reports a path that does not exist in a commit.
	ErrPathNotFound = errors.New("path not found")

	// ErrNotDirectory reports a path that exists but is not a directory.
	ErrNotDirectory = errors.New("not a directory")
)

// TreeEntry is one blob or submodule below a directory of a commit.
type TreeEntry struct {
	// Mode is the octal git file mode, such as "100644" or "120000".
	Mode string
	// Type is the object type: "blob" or "commit" for submodules.
	Type string
	// Object is the id of the entry's object.
	Object Hash
	// Size is the blob size in bytes, or -1 for submodules.
	Size int64
	// Path is the slash-separated path relative to the listed directory.
	Path string
}

// treeish names the directory dir of commit, where "." is the root.
func treeish(commit Hash, dir string) string {
	if dir == "." || dir == "" {
		return string(commit) + "^{tree}"
	}
	return string(commit) + ":" + dir
}

// Tree lists every blob and submodule below dir in commit, recursively.
// The paths are reported exactly as stored, without any interpretation, so
// the caller must validate them.
func (r *Repo) Tree(ctx context.Context, commit Hash, dir string) ([]TreeEntry, error) {
	if err := r.checkDirectory(ctx, commit, dir); err != nil {
		return nil, err
	}

	output, err := r.run(ctx, "ls-tree", "-r", "-z", "-l", treeish(commit, dir))
	if err != nil {
		return nil, fmt.Errorf("list tree: %w", err)
	}

	return parseTree(output)
}

func (r *Repo) checkDirectory(ctx context.Context, commit Hash, dir string) error {
	output, err := r.run(ctx, "cat-file", "-t", treeish(commit, dir))
	if err != nil {
		if ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("%w: %q at %s", ErrPathNotFound, dir, commit.Short())
	}

	if objectType := strings.TrimSpace(string(output)); objectType != "tree" {
		return fmt.Errorf("%w: %q at %s is a %s", ErrNotDirectory, dir, commit.Short(), objectType)
	}
	return nil
}

// parseTree reads "ls-tree -r -z -l" records:
// "<mode> SP <type> SP <object> SP+ <size> TAB <path> NUL".
func parseTree(output []byte) ([]TreeEntry, error) {
	var entries []TreeEntry
	for record := range bytes.SplitSeq(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}

		entry, err := parseTreeRecord(string(record))
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseTreeRecord(record string) (TreeEntry, error) {
	metadata, name, found := strings.Cut(record, "\t")
	fields := strings.Fields(metadata)
	if !found || len(fields) != 4 {
		return TreeEntry{}, fmt.Errorf("malformed tree record %q", record)
	}

	object, err := ParseHash(fields[2])
	if err != nil {
		return TreeEntry{}, err
	}

	size := int64(-1)
	if fields[3] != "-" {
		size, err = strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return TreeEntry{}, fmt.Errorf("malformed size in tree record %q: %w", record, err)
		}
	}

	return TreeEntry{Mode: fields[0], Type: fields[1], Object: object, Size: size, Path: name}, nil
}

// ReadBlobs streams the raw contents of the given blobs, in order, through
// a single "git cat-file --batch" process. Contents are passed through
// unchanged: no filters, attributes or text conversion apply.
func (r *Repo) ReadBlobs(ctx context.Context, ids []string, visit func(index int, content io.Reader) error) error {
	if len(ids) == 0 {
		return nil
	}

	ctx, cancel := r.git.withTimeout(ctx)
	defer cancel()

	cmd := r.git.command(ctx, r.dir, r.dir, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("read blobs: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("read blobs: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedBuffer{buffer: &stderr, limit: maxStderrBytes}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("read blobs: %w", err)
	}

	// Feed the requests concurrently: git may block on a full stdout pipe
	// before it has read all of its input.
	go func() {
		defer stdin.Close()
		for _, id := range ids {
			if _, err := io.WriteString(stdin, id+"\n"); err != nil {
				return
			}
		}
	}()

	readErr := readBatch(bufio.NewReader(stdout), ids, visit)
	if readErr != nil {
		cancel()
	}
	waitErr := cmd.Wait()

	switch {
	case readErr != nil:
		return readErr
	case waitErr != nil:
		return r.git.commandError(ctx, []string{"cat-file", "--batch"}, stderr.String(), waitErr)
	default:
		return nil
	}
}

// readBatch parses "cat-file --batch" output: a "<oid> <type> <size>" header,
// the raw content and a newline per object, or "<oid> missing".
func readBatch(output *bufio.Reader, ids []string, visit func(int, io.Reader) error) error {
	for index, id := range ids {
		header, err := output.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read object header for %s: %w", id, err)
		}

		size, err := parseBatchHeader(strings.TrimSuffix(header, "\n"), id)
		if err != nil {
			return err
		}

		content := io.LimitReader(output, size)
		if err := visit(index, content); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, content); err != nil {
			return fmt.Errorf("read object %s: %w", id, err)
		}
		if terminator, err := output.ReadByte(); err != nil || terminator != '\n' {
			return fmt.Errorf("read object %s: missing terminator", id)
		}
	}
	return nil
}

func parseBatchHeader(header, id string) (int64, error) {
	fields := strings.Fields(header)
	switch {
	case len(fields) == 2 && fields[1] == "missing":
		return 0, fmt.Errorf("read object %s: missing", id)
	case len(fields) != 3 || fields[0] != id:
		return 0, fmt.Errorf("read object %s: unexpected header %q", id, header)
	case fields[1] != "blob":
		return 0, fmt.Errorf("read object %s: is a %s, not a blob", id, fields[1])
	}

	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return 0, fmt.Errorf("read object %s: malformed size in header %q", id, header)
	}
	return size, nil
}

// DiffOptions selects the output of [Repo.Diff].
type DiffOptions struct {
	// Stat requests a diffstat summary instead of a patch.
	Stat bool
	// MaxBytes truncates the output; zero means no limit.
	MaxBytes int
}

// Diff compares dir between two commits. External diff drivers and text
// conversion filters are disabled, so no configured program runs on the
// untrusted content.
func (r *Repo) Diff(ctx context.Context, from, to Hash, dir string, options DiffOptions) ([]byte, error) {
	args := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames"}
	if options.Stat {
		args = append(args, "--stat")
	}
	args = append(args, string(from), string(to))
	if dir != "." && dir != "" {
		args = append(args, "--", dir)
	}

	output, err := r.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("diff: %w", err)
	}

	if options.MaxBytes > 0 && len(output) > options.MaxBytes {
		output = append(output[:options.MaxBytes:options.MaxBytes], []byte("\n[diff truncated]\n")...)
	}
	return output, nil
}
