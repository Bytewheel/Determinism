package replay

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Hunk is a single file operation from an apply_patch patch.
type Hunk struct {
	Type      string // "add" | "delete" | "update"
	Path      string
	MovePath  string
	Contents  string
	OldLines  []string
	NewLines  []string
	Context   string
	EndOfFile bool
}

// parsePatch parses the opencode apply_patch text format. The format is
// documented in packages/opencode/src/patch/index.ts.
func parsePatch(patchText string) ([]Hunk, error) {
	text := patchText
	// stripHeredoc equivalent: unwrap a leading heredoc-wrapped body.
	text = stripHeredoc(text)
	lines := strings.Split(text, "\n")

	var beginIdx, endIdx = -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case "*** Begin Patch":
			beginIdx = i
		case "*** End Patch":
			endIdx = i
		}
	}
	if beginIdx == -1 || endIdx == -1 || beginIdx >= endIdx {
		return nil, fmt.Errorf("invalid patch format: missing Begin/End markers")
	}

	var hunks []Hunk
	i := beginIdx + 1
	for i < endIdx {
		line := lines[i]
		var h Hunk
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			h.Type = "add"
			h.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			if h.Path == "" {
				i++
				continue
			}
			i++
			var content strings.Builder
			for i < endIdx && !strings.HasPrefix(lines[i], "***") {
				if strings.HasPrefix(lines[i], "+") {
					content.WriteString(strings.TrimPrefix(lines[i], "+"))
					content.WriteString("\n")
				}
				i++
			}
			h.Contents = strings.TrimSuffix(content.String(), "\n")
			hunks = append(hunks, h)
		case strings.HasPrefix(line, "*** Delete File:"):
			h.Type = "delete"
			h.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			if h.Path == "" {
				i++
				continue
			}
			i++
			hunks = append(hunks, h)
		case strings.HasPrefix(line, "*** Update File:"):
			h.Type = "update"
			h.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			if h.Path == "" {
				i++
				continue
			}
			i++
			if i < endIdx && strings.HasPrefix(lines[i], "*** Move to:") {
				h.MovePath = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to:"))
				i++
			}
			// parse context chunks
			var oldLines, newLines []string
			context := ""
			endOfFile := false
			for i < endIdx && !strings.HasPrefix(lines[i], "***") {
				l := lines[i]
				switch {
				case strings.HasPrefix(l, "@@"):
					context = strings.TrimSpace(strings.TrimPrefix(l, "@@"))
					// start a new chunk
					if len(oldLines) > 0 || len(newLines) > 0 {
						hunks = append(hunks, cloneUpdateHunk(h, oldLines, newLines, context, endOfFile))
						oldLines, newLines = nil, nil
					}
					i++
				case l == "*** End of File":
					endOfFile = true
					i++
				case strings.HasPrefix(l, " "):
					oldLines = append(oldLines, strings.TrimPrefix(l, " "))
					newLines = append(newLines, strings.TrimPrefix(l, " "))
					i++
				case strings.HasPrefix(l, "-"):
					oldLines = append(oldLines, strings.TrimPrefix(l, "-"))
					i++
				case strings.HasPrefix(l, "+"):
					newLines = append(newLines, strings.TrimPrefix(l, "+"))
					i++
				default:
					i++
				}
			}
			hunks = append(hunks, cloneUpdateHunk(h, oldLines, newLines, context, endOfFile))
		default:
			i++
		}
	}
	return hunks, nil
}

func cloneUpdateHunk(h Hunk, oldLines, newLines []string, context string, endOfFile bool) Hunk {
	c := h
	c.OldLines = oldLines
	c.NewLines = newLines
	c.Context = context
	c.EndOfFile = endOfFile
	return c
}

func stripHeredoc(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return input
	}
	// Match: cat <<'EOF'  OR  <<EOF   on first non-empty line
	first := strings.TrimSpace(lines[0])
	var delim string
	if strings.HasPrefix(first, "cat <<") || strings.HasPrefix(first, "<<") {
		rest := strings.TrimSpace(strings.TrimPrefix(first, "cat"))
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "<<") {
			rest = strings.TrimPrefix(rest, "<<")
			rest = strings.Trim(rest, "'\"")
			delim = rest
		}
	}
	if delim == "" {
		return input
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == delim {
			return strings.Join(lines[1:i], "\n")
		}
	}
	return input
}

// ApplyPatch applies parsed hunks to the filesystem rooted at base.
func ApplyPatch(base string, patchText string) ([]string, error) {
	return ApplyPatchResolved(base, patchText, func(p string) (string, error) {
		return resolve(base, p), nil
	})
}

// ApplyPatchResolved applies parsed hunks using a custom path resolver, which
// allows sandboxing (Rewriter) or relocating absolute paths.
func ApplyPatchResolved(base string, patchText string, resolvePath func(string) (string, error)) ([]string, error) {
	hunks, err := parsePatch(patchText)
	if err != nil {
		return nil, err
	}
	var ops []string
	for _, h := range hunks {
		src, err := resolvePath(h.Path)
		if err != nil {
			return ops, err
		}
		switch h.Type {
		case "add":
			if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
				return ops, err
			}
			if err := os.WriteFile(src, []byte(h.Contents), 0o644); err != nil {
				return ops, err
			}
			ops = append(ops, "added "+h.Path)
		case "delete":
			if err := os.Remove(src); err != nil {
				return ops, err
			}
			ops = append(ops, "deleted "+h.Path)
		case "update":
			if h.MovePath != "" {
				dst, err := resolvePath(h.MovePath)
				if err != nil {
					return ops, err
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return ops, err
				}
				if err := os.Rename(src, dst); err != nil {
					return ops, err
				}
				src = dst
				ops = append(ops, "moved "+h.Path+" -> "+h.MovePath)
			}
			if err := applyUpdateChunks(src, h.OldLines, h.NewLines); err != nil {
				return ops, err
			}
			ops = append(ops, "updated "+h.Path)
		}
	}
	return ops, nil
}

// applyUpdateChunks applies a single old->new line transformation to a file.
func applyUpdateChunks(path string, oldLines, newLines []string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// If old is empty this is an append at end-of-file marker.
	if len(oldLines) == 0 {
		var b strings.Builder
		b.Write(content)
		if !strings.HasSuffix(contentStr(content), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(strings.Join(newLines, "\n"))
		return os.WriteFile(path, []byte(b.String()), 0o644)
	}

	fileLines := strings.Split(contentStr(content), "\n")
	// drop trailing empty element from trailing newline
	if n := len(fileLines); n > 0 && fileLines[n-1] == "" {
		fileLines = fileLines[:n-1]
	}
	idx := findSequence(fileLines, oldLines)
	if idx == -1 {
		return fmt.Errorf("patch context not found in %s", path)
	}
	replacement := append([]string{}, fileLines[:idx]...)
	replacement = append(replacement, newLines...)
	replacement = append(replacement, fileLines[idx+len(oldLines):]...)
	out := strings.Join(replacement, "\n") + "\n"
	return os.WriteFile(path, []byte(out), 0o644)
}

func contentStr(b []byte) string { return string(b) }

// findSequence returns the index of oldLines within fileLines, or -1.
func findSequence(fileLines, oldLines []string) int {
	if len(oldLines) == 0 {
		return 0
	}
	for i := 0; i+len(oldLines) <= len(fileLines); i++ {
		match := true
		for j := range oldLines {
			if fileLines[i+j] != oldLines[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// resolve joins base and a possibly-absolute path. Absolute paths are honored
// as-is (they mirror the session's original layout).
func resolve(base, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(base, p)
}
