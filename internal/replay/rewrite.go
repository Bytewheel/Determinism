package replay

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bytewheel/determinism/internal/cache"
)

// Path classification for recorded absolute paths.
const (
	kindSession = iota // at/under SessionDir
	kindSibling        // at/under SiblingRoot but outside SessionDir
	kindOutside        // anywhere else (system paths, other projects)
)

// Rewriter relocates the file paths recorded in a session into a sandbox
// directory so a replay's writes are contained and cannot touch the original
// session paths.
//
// Rules:
//   - Relative paths resolve under TargetDir (the mirror of SessionDir).
//   - Absolute paths at or under SessionDir are remapped to the mirror path
//     under TargetDir. On first access the real file or directory is copied
//     into the mirror (copy-on-first-access, skipped for dry runs) so replay
//     commands can read the session's source without touching the original.
//   - Relative paths that escape the session directory (e.g. "../repo/file")
//     are resolved against SessionDir; sibling references are rewritten.
//   - Absolute paths under SiblingRoot (the parent of SessionDir) reference
//     sibling repositories. Their handling depends on the configured policy:
//     cached snapshots (Cache set), real-path access (Dangerous + Allowed), or
//     graceful rejection (neither).
//   - Absolute paths outside both are rejected by Resolve, but left untouched
//     by command-text rewriting (system paths like /dev/null must still work).
type Rewriter struct {
	// SessionDir is the original session working directory (absolute). Empty
	// when the session has no recorded directory (e.g. chat transcripts).
	SessionDir string
	// TargetDir is the sandbox root (absolute) that mirrors SessionDir.
	TargetDir string
	// WorkDir is where relative paths are anchored for command-text rewriting.
	// It is the sandbox mirror of SessionDir (TargetDir when SessionDir is
	// empty), which is also the directory commands are executed from.
	WorkDir string
	// SiblingRoot is the parent of SessionDir; its immediate children are the
	// sibling repositories. Empty when SessionDir is empty.
	SiblingRoot string
	// Cache, when set, snapshots sibling repositories (creating the snapshot
	// from the real paths when absent) and resolves sibling references to the
	// extracted cache instead of the real location.
	Cache *cache.Cache
	// Dangerous enables real-path access to sibling repositories. Siblings are
	// only accessible when present in Allowed.
	Dangerous bool
	// Allowed holds sibling repository paths permitted for real-path access
	// when Dangerous is set.
	Allowed map[string]bool
	// MountsDir is where extracted sibling cache snapshots live.
	MountsDir string
	// Dry, when true, suppresses filesystem side effects (sibling snapshot
	// creation/sync) so a dry run never reads or writes the real paths.
	Dry bool

	mounts   map[string]string // sibling root -> extracted cache mount
	accessed []string          // siblings touched at real paths (dangerous mode)
}

// NewRewriter returns a Rewriter mapping SessionDir onto TargetDir. Both are
// cleaned to absolute form.
func NewRewriter(sessionDir, targetDir string) (*Rewriter, error) {
	var s string
	var err error
	if sessionDir != "" {
		s, err = filepath.Abs(sessionDir)
		if err != nil {
			return nil, err
		}
	}
	t, err := filepath.Abs(targetDir)
	if err != nil {
		return nil, err
	}
	r := &Rewriter{
		SessionDir: s,
		TargetDir:  t,
		Allowed:    map[string]bool{},
		mounts:     map[string]string{},
	}
	r.WorkDir = t
	if s != "" {
		r.SiblingRoot = filepath.Dir(s)
	}
	r.MountsDir = filepath.Join(t, ".determinism", "siblings")
	return r, nil
}

// classify sorts an absolute path into a kind and the sibling unit it belongs
// to (only meaningful for kindSibling).
func (r *Rewriter) classify(p string) (kind int, unit string) {
	clean := filepath.Clean(p)
	if r.SessionDir != "" && under(r.SessionDir, clean) {
		return kindSession, r.SessionDir
	}
	if r.SiblingRoot != "" && under(r.SiblingRoot, clean) {
		rel, _ := filepath.Rel(r.SiblingRoot, clean)
		parts := strings.SplitN(rel, string(filepath.Separator), 2)
		return kindSibling, filepath.Join(r.SiblingRoot, parts[0])
	}
	return kindOutside, ""
}

// Resolve maps a recorded path to the sandboxed filesystem path. Strict: any
// path outside SessionDir that cannot be handled by the configured sibling
// policy is rejected.
func (r *Rewriter) Resolve(p string) (string, error) {
	if p == "" {
		return r.TargetDir, nil
	}
	if !filepath.IsAbs(p) {
		joined := filepath.Join(r.TargetDir, p)
		rel, err := filepath.Rel(r.TargetDir, joined)
		if err != nil || outside(rel) {
			return "", fmt.Errorf("path %s escapes sandbox %s", p, r.TargetDir)
		}
		if r.SessionDir != "" && !r.Dry {
			// Copy-on-first-access so relative edits/patches apply against the
			// original content.
			_ = r.ensureMirrored(filepath.Join(r.SessionDir, p), joined)
		}
		return joined, nil
	}
	return r.resolveAbs(filepath.Clean(p), true)
}

// ResolveLoose maps a path for command-text rewriting. Absolute paths outside
// the sandbox and sibling roots are returned unchanged (system paths must keep
// working); everything else follows the same policy as Resolve.
func (r *Rewriter) ResolveLoose(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return p, nil
	}
	return r.resolveAbs(filepath.Clean(p), false)
}

func (r *Rewriter) resolveAbs(p string, strict bool) (string, error) {
	kind, unit := r.classify(p)
	switch kind {
	case kindSession:
		rel, _ := filepath.Rel(r.SessionDir, p)
		target := filepath.Join(r.TargetDir, rel)
		if !r.Dry {
			_ = r.ensureMirrored(p, target)
		}
		return target, nil
	case kindSibling:
		return r.resolveSibling(unit, p)
	default:
		if strict {
			return "", fmt.Errorf("path %s is outside session directory %s", p, r.SessionDir)
		}
		return p, nil
	}
}

// maxMirroredFile bounds how large a real file may be copied into the sandbox
// mirror on first access, keeping copy-on-first-access bounded.
const maxMirroredFile = 200 << 20

// ensureMirrored copies a real session path into its sandbox mirror on first
// access (copy-on-first-access) so replay commands can read the session's
// source. Directories are created empty; files are copied unless they live
// under a version-control or generated directory, exceed maxMirroredFile, or
// are not regular files. Missing sources are left alone (writes create them).
// Best-effort: errors are ignored so an unwritable/unreadable path never
// blocks replay.
func (r *Rewriter) ensureMirrored(source, target string) error {
	if _, err := os.Lstat(target); err == nil {
		return nil
	}
	info, err := os.Lstat(source)
	if err != nil || info == nil {
		return nil
	}
	if info.IsDir() {
		return os.MkdirAll(target, 0o755)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	clean := filepath.Clean(source)
	for _, seg := range strings.Split(clean, string(filepath.Separator)) {
		switch seg {
		case ".git", ".hg", ".svn", "node_modules", "build", "builddir",
			"_build", "dist", "target", ".cache", "__pycache__", ".determinism":
			return nil
		}
	}
	if info.Size() > maxMirroredFile {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return nil
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// resolveSibling applies the sibling policy: cached snapshot, real-path access
// (dangerous, if allowed), or graceful rejection.
func (r *Rewriter) resolveSibling(unit, p string) (string, error) {
	if r.Dangerous {
		if r.Allowed != nil && r.Allowed[unit] {
			if r.Cache != nil && !r.Dry {
				// dangerous + cache: keep the snapshot in real-time sync. Seed
				// it on first access; SyncSiblings refreshes it afterwards.
				if err := r.ensureSnapshot(unit); err != nil {
					return "", err
				}
			}
			r.track(unit)
			return p, nil
		}
		return "", fmt.Errorf("sibling %s not allowed in dangerous mode; approve it or use --allow-dir %s", unit, unit)
	}
	if r.Cache == nil {
		return "", fmt.Errorf("path %s references sibling %s outside the sandbox; use --cache to snapshot it or --dangerous to allow real-path access", p, unit)
	}
	mount, err := r.ensureMount(unit)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(unit, p)
	if err != nil {
		return "", err
	}
	return filepath.Join(mount, rel), nil
}

// ensureSnapshot creates the compressed cache snapshot for a sibling repository
// if it does not already exist. It does not extract a mount.
func (r *Rewriter) ensureSnapshot(unit string) error {
	key := cache.SiblingKey(unit)
	if r.Cache.Exists(key) {
		return nil
	}
	return r.Cache.Save(key, unit, true)
}

// ensureMount returns the extracted cache mount for a sibling repository,
// creating the compressed snapshot from the real path first if needed.
func (r *Rewriter) ensureMount(unit string) (string, error) {
	if m, ok := r.mounts[unit]; ok {
		return m, nil
	}
	key := cache.SiblingKey(unit)
	mount := filepath.Join(r.MountsDir, filepath.Base(unit))
	if err := os.MkdirAll(mount, 0o755); err != nil {
		return "", err
	}
	if !r.Cache.Exists(key) && !r.Dry {
		if err := r.Cache.Save(key, unit, true); err != nil {
			return "", err
		}
	}
	if !r.Dry {
		if err := r.Cache.Restore(key, mount); err != nil {
			return "", err
		}
	}
	r.mounts[unit] = mount
	return mount, nil
}

func (r *Rewriter) track(unit string) {
	for _, a := range r.accessed {
		if a == unit {
			return
		}
	}
	r.accessed = append(r.accessed, unit)
}

// SyncSiblings re-snapshots sibling repositories that were touched at their
// real paths, keeping the compressed cache in sync in real time. Only active
// when Dangerous and Cache are both set.
func (r *Rewriter) SyncSiblings() error {
	if !r.Dangerous || r.Cache == nil || r.Dry {
		return nil
	}
	for _, unit := range r.accessed {
		if err := r.Cache.Save(cache.SiblingKey(unit), unit, true); err != nil {
			return err
		}
	}
	return nil
}

// ReferencedSiblings returns the distinct sibling repository roots referenced
// by absolute or escaping-relative paths in a shell command.
func (r *Rewriter) ReferencedSiblings(cmd string) []string {
	seen := map[string]bool{}
	var out []string
	_ = r.walkWords(cmd, nil, func(word string) error {
		_, path := splitPath(word)
		if path == "" {
			if r.SessionDir != "" && strings.HasPrefix(word, ".."+string(filepath.Separator)) {
				_, unit := r.classify(filepath.Join(r.SessionDir, word))
				if unit != "" && !seen[unit] {
					seen[unit] = true
					out = append(out, unit)
				}
			}
			return nil
		}
		if filepath.IsAbs(path) {
			kind, unit := r.classify(path)
			if kind == kindSibling && !seen[unit] {
				seen[unit] = true
				out = append(out, unit)
			}
		}
		return nil
	})
	return out
}

// RewriteCommand rewrites absolute paths inside a shell command so that they
// resolve inside the sandbox. System paths and relative paths are preserved.
func (r *Rewriter) RewriteCommand(cmd string) (string, error) {
	var out strings.Builder
	err := r.walkWords(cmd, func(sep string) { out.WriteString(sep) }, func(word string) error {
		nw, err := r.rewriteWord(word)
		if err != nil {
			return err
		}
		out.WriteString(nw)
		return nil
	})
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// rewriteWord rewrites the absolute path portion of a single shell word,
// preserving redirect/assignment prefixes and quoting. Relative words that
// escape the session directory (e.g. "../sibling-repo/x") are resolved against
// SessionDir and rewritten to the configured sibling location when possible;
// relative words inside the session are left untouched (the runtime working
// directory handles them).
func (r *Rewriter) rewriteWord(word string) (string, error) {
	inner, openQ, closeQ := unquote(word)
	prefix, path := splitPath(inner)
	if path == "" {
		if r.SessionDir == "" {
			return word, nil
		}
		resolved, ok, err := r.rewriteRelative(inner)
		if err != nil {
			return "", err
		}
		if !ok {
			return word, nil
		}
		out := resolved
		if needsQuote(out, openQ) {
			return "'" + out + "'", nil
		}
		return openQ + out + closeQ, nil
	}
	resolved, err := r.ResolveLoose(path)
	if err != nil {
		return "", err
	}
	if resolved == path {
		return word, nil
	}
	out := prefix + resolved
	if needsQuote(out, openQ) {
		return "'" + out + "'", nil
	}
	return openQ + out + closeQ, nil
}

// rewriteRelative rewrites a relative path word that escapes the session
// directory. It returns ok=false when the word is not an escaping path or
// resolves back inside the session.
func (r *Rewriter) rewriteRelative(w string) (string, bool, error) {
	if !strings.HasPrefix(w, ".."+string(filepath.Separator)) {
		return "", false, nil
	}
	real := filepath.Join(r.SessionDir, w)
	kind, unit := r.classify(real)
	if kind != kindSibling {
		return "", false, nil
	}
	if unit == r.SiblingRoot {
		return r.TargetDir, true, nil
	}
	resolved, err := r.resolveSibling(unit, real)
	if err != nil {
		return "", true, err
	}
	return resolved, true, nil
}

// under reports whether path is at or under dir.
func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !outside(rel)
}

// outside reports whether a Rel() result escapes its base (".." or a
// "../"-prefixed sibling).
func outside(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// splitPath separates a shell word into its non-path prefix (redirect operator
// or assignment like "2>" or "FOO=") and an absolute path, if the word carries
// one.
func splitPath(w string) (prefix, path string) {
	for _, pre := range []string{"2>>", "1>>", "&>>", "2>", "1>", "&>", ">>", ">", "<"} {
		if strings.HasPrefix(w, pre) {
			rest := strings.TrimPrefix(w, pre)
			if strings.HasPrefix(rest, "/") {
				return pre, rest
			}
			return w, ""
		}
	}
	if i := strings.IndexByte(w, '='); i > 0 {
		lhs, rhs := w[:i], w[i+1:]
		if strings.HasPrefix(rhs, "/") && !strings.Contains(lhs, "/") &&
			!strings.ContainsAny(lhs, "$(){}[]*?") {
			return w[:i+1], rhs
		}
		return w, ""
	}
	if strings.HasPrefix(w, "/") {
		return "", w
	}
	return w, ""
}

// unquote strips a matched pair of surrounding single or double quotes.
func unquote(w string) (inner, open, close string) {
	if len(w) >= 2 && w[0] == '\'' && w[len(w)-1] == '\'' {
		return w[1 : len(w)-1], "'", "'"
	}
	if len(w) >= 2 && w[0] == '"' && w[len(w)-1] == '"' {
		return w[1 : len(w)-1], "\"", "\""
	}
	return w, "", ""
}

func needsQuote(s, openQ string) bool {
	if openQ != "" {
		return false
	}
	return strings.ContainsAny(s, " \t\"'`$;&|()<>*?")
}

// heredocDelim extracts the heredoc delimiter from a word like "<<EOF" or
// "<<-END". Returns ok=false for anything that is not a heredoc opener.
func heredocDelim(word string) (string, bool) {
	w := word
	switch {
	case strings.HasPrefix(w, "<<-"):
		w = strings.TrimPrefix(w, "<<-")
	case strings.HasPrefix(w, "<<"):
		w = strings.TrimPrefix(w, "<<")
	default:
		return "", false
	}
	if len(w) >= 2 && ((w[0] == '\'' && w[len(w)-1] == '\'') || (w[0] == '"' && w[len(w)-1] == '"')) {
		w = w[1 : len(w)-1]
	}
	if w == "" {
		return "", false
	}
	for _, c := range w {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return "", false
		}
	}
	return w, true
}

// walkWords scans a shell command, calling emit for verbatim chunks
// (whitespace, operators, newlines, heredoc openers, and full heredoc bodies
// including the delimiter) and fn for each command word. Heredoc delimiters
// must be attached to the opener (cat <<EOF), matching how opencode records
// them. emit may be nil when only the words matter.
func (r *Rewriter) walkWords(cmd string, emit func(string), fn func(string) error) error {
	i, n := 0, len(cmd)
	heredoc := ""
	lineStart := true
	verbatim := func(s string) {
		if emit != nil && s != "" {
			emit(s)
		}
	}
	for i < n {
		if heredoc != "" {
			if lineStart && strings.HasPrefix(cmd[i:], heredoc) {
				rest := cmd[i:]
				if len(rest) == len(heredoc) || rest[len(heredoc)] == '\n' {
					verbatim(cmd[i : i+len(heredoc)])
					i += len(heredoc)
					heredoc = ""
					continue
				}
			}
			j := strings.IndexByte(cmd[i:], '\n')
			if j < 0 {
				verbatim(cmd[i:])
				i = n
			} else {
				verbatim(cmd[i : i+j+1])
				i += j + 1
			}
			lineStart = true
			continue
		}
		c := cmd[i]
		if c == '\n' {
			verbatim("\n")
			i++
			lineStart = true
			continue
		}
		if isSpace(c) || isOperator(c) {
			s := i
			for i < n && (isSpace(cmd[i]) || isOperator(cmd[i])) {
				i++
			}
			verbatim(cmd[s:i])
			lineStart = false
			continue
		}
		lineStart = false
		quote := byte(0)
		var sb strings.Builder
		for i < n {
			c := cmd[i]
			if quote == 0 {
				if c == '\'' || c == '"' {
					quote = c
					sb.WriteByte(c)
					i++
					continue
				}
				if isSpace(c) || isOperator(c) {
					break
				}
				if c == '\\' && i+1 < n {
					sb.WriteByte(c)
					sb.WriteByte(cmd[i+1])
					i += 2
					continue
				}
				sb.WriteByte(c)
				i++
				continue
			}
			sb.WriteByte(c)
			if c == quote {
				quote = 0
			} else if quote == '"' && c == '\\' && i+1 < n {
				sb.WriteByte(cmd[i+1])
				i++
			}
			i++
		}
		word := sb.String()
		if d, ok := heredocDelim(word); ok {
			verbatim(word)
			heredoc = d
			continue
		}
		if err := fn(word); err != nil {
			return err
		}
	}
	return nil
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t'
}

func isOperator(c byte) bool {
	switch c {
	case '&', '|', ';', '(', ')':
		return true
	}
	return false
}
