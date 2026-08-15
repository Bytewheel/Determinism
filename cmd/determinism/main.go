// Command determinism ingests an opencode AI session and deterministically
// replays every command and file operation (write, edit, delete, overwrite)
// the session performed.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bytewheel/determinism/internal/cache"
	"bytewheel/determinism/internal/plan"
	"bytewheel/determinism/internal/replay"
	"bytewheel/determinism/internal/session"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `determinism - deterministic replay of opencode AI sessions

Usage:
  determinism run <session|name|prompt|file.md> [flags]
  determinism plan <session|name|prompt|file.md> [flags]
  determinism export <session|name|prompt|file.md> [--out file] [--db path]
  determinism list [--db path] [--limit n]
  determinism cache save <key> <dir> [--cache-dir path]
  determinism cache restore <key> <dir> [--cache-dir path]
  determinism cache list [--cache-dir path]
  determinism cache info <key> [--cache-dir path]

A session reference can be:
  - an opencode session id (ses_...)
  - a session name (slug), e.g. swift-pixel
  - a title substring
  - a free-text initial prompt
  - a path to a chat-text .md transcript

Flags:
  --db path          opencode database (default ~/.local/share/opencode/opencode.db)
  --dir path         working directory for replay (default: session directory)
  --rewrite-dir path sandbox root (default ~/.local/share/determinism/<name>)
  --cache            snapshot sibling repos into the cache; reference those
                     snapshots instead of the real paths
  --cache-dir path   cache directory (default ~/.cache/determinism)
  --dangerous        access sibling repos at their real absolute paths
                     (each referenced sibling requires approval)
  --allow-dir path   pre-approve a sibling dir for --dangerous (repeatable)
  --dry-run          print actions without executing them
  --subprompts       execute subprompts (task/subagent sessions)
  --strict           stop at the first failure instead of failing gracefully
  --shell path       shell used for bash replay (default /bin/sh)
  --out file         write plan to a file (plan) or write the transcript to a
                     file (export; default <slug>.md, use '-' for stdout)
`)
}

type opts struct {
	db         string
	dir        string
	rewriteDir string
	dryRun     bool
	subprompts bool
	useCache   bool
	cacheDir   string
	strict     bool
	shell      string
	out        string
	limit      int
	dangerous  bool
	allowDirs  []string
}

func parseFlags(args []string) (opts, []string, error) {
	// Go's flag package stops at the first non-flag argument; reorder so flags
	// are consumed regardless of position and return the positionals.
	flags, pos, err := splitFlags(args)
	if err != nil {
		return opts{}, nil, err
	}
	o, err := parseFlagSet(flags)
	return o, pos, err
}

func parseFlagSet(args []string) (opts, error) {
	var o opts
	fs := flag.NewFlagSet("determinism", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&o.db, "db", "", "")
	fs.StringVar(&o.dir, "dir", "", "")
	fs.StringVar(&o.rewriteDir, "rewrite-dir", "", "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.subprompts, "subprompts", false, "")
	fs.BoolVar(&o.useCache, "cache", false, "")
	fs.StringVar(&o.cacheDir, "cache-dir", "", "")
	fs.BoolVar(&o.strict, "strict", false, "")
	fs.StringVar(&o.shell, "shell", "", "")
	fs.StringVar(&o.out, "out", "", "")
	fs.IntVar(&o.limit, "limit", 50, "")
	fs.BoolVar(&o.dangerous, "dangerous", false, "")
	fs.Var(&stringSlice{o: &o.allowDirs}, "allow-dir", "")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.cacheDir == "" {
		home, _ := os.UserHomeDir()
		o.cacheDir = filepath.Join(home, ".cache", "determinism")
	}
	return o, nil
}

// splitFlags separates flag tokens (with their values) from positional args.
// Flags that take a value are paired with the following argument.
func splitFlags(args []string) (flags, pos []string, err error) {
	valueFlags := map[string]bool{
		"-db": true, "-dir": true, "-cache-dir": true, "-shell": true,
		"-out": true, "-limit": true, "--db": true, "--dir": true,
		"--cache-dir": true, "--shell": true, "--out": true, "--limit": true,
		"-rewrite-dir": true, "--rewrite-dir": true,
		"-allow-dir": true, "--allow-dir": true,
	}
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			if strings.Contains(a, "=") {
				flags = append(flags, a)
				i++
				continue
			}
			flags = append(flags, a)
			if valueFlags[a] {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("flag %s requires a value", a)
				}
				flags = append(flags, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		pos = append(pos, a)
		i++
	}
	return flags, pos, nil
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "run", "plan":
		if len(rest) < 1 {
			usage()
			return fmt.Errorf("%s requires a session reference", cmd)
		}
		o, pos, err := parseFlags(rest[1:])
		if err != nil {
			return err
		}
		ref := rest[0]
		if len(pos) > 0 {
			ref = pos[0]
		}
		if cmd == "plan" {
			return runPlan(ref, o)
		}
		return runReplay(ref, o)

	case "export":
		if len(rest) < 1 {
			usage()
			return fmt.Errorf("export requires a session reference")
		}
		o, pos, err := parseFlags(rest[1:])
		if err != nil {
			return err
		}
		ref := rest[0]
		if len(pos) > 0 {
			ref = pos[0]
		}
		return runExport(ref, o)

	case "list":
		o, _, err := parseFlags(rest)
		if err != nil {
			return err
		}
		return runList(o)

	case "cache":
		if len(rest) < 1 {
			return fmt.Errorf("cache requires a subcommand (save|restore|list|info)")
		}
		o, pos, err := parseFlags(rest[1:])
		if err != nil {
			return err
		}
		return runCache(rest[0], pos, o)

	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// loadSession resolves a reference into a session, using either the chat text
// format (a .md/.txt file) or the opencode database.
func loadSession(ref string, o opts) (*session.Session, session.Source, error) {
	if fi, err := os.Stat(ref); err == nil && !fi.IsDir() {
		ext := strings.ToLower(filepath.Ext(ref))
		if ext == ".md" || ext == ".txt" || ext == ".markdown" {
			s, err := session.ParseChatText(ref)
			if err != nil {
				return nil, "", err
			}
			return s, session.SourceChat, nil
		}
	}
	ld, err := session.Open(o.db)
	if err != nil {
		return nil, "", err
	}
	defer ld.Close()
	// A ref that looks like a ses_ id or a slug is treated as an id/name.
	lk := session.Lookup{}
	if strings.HasPrefix(ref, "ses_") {
		lk.ID = ref
	} else if isSlugLike(ref) {
		lk.Name = ref
	} else if strings.Contains(ref, " ") || strings.Contains(ref, "/") {
		lk.Prompt = ref
	} else {
		lk.Title = ref
	}
	s, err := ld.Find(lk)
	if err != nil {
		return nil, "", err
	}
	return s, session.SourceDB, nil
}

func isSlugLike(s string) bool {
	if s == "" || strings.Contains(s, " ") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

// stringSlice implements flag.Value for a repeatable string flag.
type stringSlice struct{ o *[]string }

func (s *stringSlice) String() string { return strings.Join(*s.o, ",") }
func (s *stringSlice) Set(v string) error {
	*s.o = append(*s.o, v)
	return nil
}

func newEngine(o opts, ld *session.Loader, s *session.Session) (*replay.Engine, error) {
	e := &replay.Engine{
		WorkDir:        o.dir,
		Shell:          o.shell,
		Subprompts:     o.subprompts,
		FailGracefully: !o.strict,
		DryRun:         o.dryRun,
		Log:            func(f string, a ...any) { fmt.Fprintf(os.Stdout, f+"\n", a...) },
	}
	if o.rewriteDir != "" {
		rw, err := replay.NewRewriter(s.Directory, o.rewriteDir)
		if err != nil {
			return nil, err
		}
		rw.Dry = o.dryRun
		rw.Dangerous = o.dangerous
		if o.useCache {
			c, err := cache.New(o.cacheDir)
			if err != nil {
				return nil, err
			}
			rw.Cache = c
		}
		allowed, err := allowedSiblings(o, s, rw)
		if err != nil {
			return nil, err
		}
		rw.Allowed = allowed
		e.Rewrite = rw
	}
	if ld != nil {
		e.LoadChild = func(id string) (*session.Session, error) {
			child, err := ld.Load(id)
			if err != nil {
				return nil, err
			}
			if child.Directory == "" {
				child.Directory = s.Directory
			}
			return child, nil
		}
		e.FindChild = func(parentID, description string) (string, error) {
			return ld.FindChild(parentID, description)
		}
	}
	return e, nil
}

// allowedSiblings collects the sibling directories the replay may access at
// their real paths. --allow-dir pre-approves dirs; anything referenced by the
// session but not pre-approved is prompted for unless running non-interactively
// (in which case it is rejected).
func allowedSiblings(o opts, s *session.Session, rw *replay.Rewriter) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, d := range o.allowDirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			return nil, err
		}
		allowed[filepath.Clean(abs)] = true
	}
	if !o.dangerous {
		return allowed, nil
	}
	refs := referencedSiblings(s, rw)
	sort.Strings(refs)
	var missing []string
	for _, ref := range refs {
		if !allowed[ref] {
			missing = append(missing, ref)
		}
	}
	if len(missing) == 0 {
		return allowed, nil
	}
	if o.dryRun {
		fmt.Fprintf(os.Stderr, "WARNING: --dangerous: session references sibling dirs not covered by --allow-dir:\n")
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "  %s\n", m)
		}
		fmt.Fprintf(os.Stderr, "dry-run; skipping approval prompt\n")
		return allowed, nil
	}
	return promptSiblings(missing, allowed)
}

// referencedSiblings returns the distinct sibling directories the session
// touches through bash commands or file tool paths.
func referencedSiblings(s *session.Session, rw *replay.Rewriter) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || !filepath.IsAbs(p) {
			return
		}
		for _, d := range rw.ReferencedSiblings(p) {
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	for _, m := range s.Messages {
		for _, p := range m.Parts {
			if p.Type != "tool" {
				continue
			}
			switch p.ToolName {
			case "bash":
				var in struct {
					Command string `json:"command"`
				}
				if err := json.Unmarshal(p.Input, &in); err == nil {
					for _, d := range rw.ReferencedSiblings(in.Command) {
						if !seen[d] {
							seen[d] = true
							out = append(out, d)
						}
					}
				}
			case "write", "edit":
				add(toolPath(p.Input))
			case "apply_patch":
				for _, d := range rw.ReferencedSiblings(toolPatch(p.Input)) {
					if !seen[d] {
						seen[d] = true
						out = append(out, d)
					}
				}
			}
		}
	}
	return out
}

func toolPath(raw []byte) string {
	var in struct {
		FilePath string `json:"filePath"`
		Path     string `json:"path"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	if in.FilePath != "" {
		return in.FilePath
	}
	return in.Path
}

func toolPatch(raw []byte) string {
	var in struct {
		PatchText string `json:"patchText"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return ""
	}
	return in.PatchText
}

// promptSiblings asks the user which referenced sibling directories to allow
// at their real paths under --dangerous.
func promptSiblings(missing []string, allowed map[string]bool) (map[string]bool, error) {
	fmt.Fprintln(os.Stderr, "WARNING: --dangerous will access these sibling directories at their REAL paths.")
	fmt.Fprintln(os.Stderr, "The replay can write to them. Which do you want to allow?")
	for i, m := range missing {
		fmt.Fprintf(os.Stderr, "  [%d] %s\n", i+1, m)
	}
	fmt.Fprintln(os.Stderr, "Enter numbers (comma-separated), 'all', or 'none':")
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return allowed, fmt.Errorf("no input; aborting dangerous access (use --allow-dir to pre-approve)")
	}
	line = strings.ToLower(strings.TrimSpace(line))
	switch line {
	case "":
		return allowed, fmt.Errorf("aborting dangerous access (use --allow-dir to pre-approve)")
	case "all":
		for _, m := range missing {
			allowed[m] = true
		}
		return allowed, nil
	case "none":
		return allowed, nil
	}
	for _, tok := range strings.Split(line, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		var idx int
		if _, err := fmt.Sscanf(tok, "%d", &idx); err != nil || idx < 1 || idx > len(missing) {
			continue
		}
		allowed[missing[idx-1]] = true
	}
	return allowed, nil
}

// defaultSandboxDir returns the default sandbox root for a session name:
// ~/.local/share/determinism/<name> on Unix.
func defaultSandboxDir(name string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	base := filepath.Join(home, ".local", "share", "determinism")
	safe := strings.NewReplacer("/", "_", " ", "_", ":", "_", "\\", "_").Replace(name)
	return filepath.Join(base, safe)
}

func runPlan(ref string, o opts) error {
	s, src, err := loadSession(ref, o)
	if err != nil {
		return err
	}
	steps := plan.Build(s)
	md := plan.RenderMarkdown(s, steps)
	if o.out != "" {
		if err := os.WriteFile(o.out, []byte(md), 0o644); err != nil {
			return err
		}
		fmt.Printf("plan written to %s (%d steps)\n", o.out, len(steps))
		return nil
	}
	fmt.Print(md)
	fmt.Printf("\n(source: %s)\n", src)
	return nil
}

// runExport writes a session to a chat-text markdown transcript that can be
// edited by hand and reimported via `run`/`plan <file.md>`.
func runExport(ref string, o opts) error {
	s, src, err := loadSession(ref, o)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := session.WriteChatText(&buf, s); err != nil {
		return err
	}
	out := o.out
	if out == "" {
		out = s.Slug
		if out == "" {
			out = s.ID
		}
		if out == "" {
			out = "session"
		}
		out += ".md"
	}
	if out == "-" {
		fmt.Print(buf.String())
		return nil
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	n := 0
	for _, m := range s.Messages {
		n += len(m.Parts)
	}
	fmt.Printf("exported %d messages / %d parts to %s (source: %s)\n", len(s.Messages), n, out, src)
	fmt.Printf("edit %s by hand, then reimport with: determinism run %s\n", out, out)
	return nil
}

func runReplay(ref string, o opts) error {
	s, src, err := loadSession(ref, o)
	if err != nil {
		return err
	}
	if o.rewriteDir == "" && o.dir == "" {
		name := s.Slug
		if name == "" {
			name = s.ID
		}
		o.rewriteDir = defaultSandboxDir(name)
	}
	if o.rewriteDir == "" {
		o.rewriteDir = o.dir
	}
	// Sandbox mode: the replay workdir IS the sandbox root.
	o.dir = o.rewriteDir
	if o.dir != "" {
		if err := os.MkdirAll(o.dir, 0o755); err != nil {
			return err
		}
	}

	// Loader kept open only when the source is the DB (needed for subprompts).
	var ld *session.Loader
	if src == session.SourceDB && o.subprompts {
		ld, err = session.Open(o.db)
		if err != nil {
			return err
		}
		defer ld.Close()
	}

	e, err := newEngine(o, ld, s)
	if err != nil {
		return err
	}

	// Warn early when the session references sibling repos that the configured
	// policy would reject, so the user can decide before running.
	if e.Rewrite != nil {
		refs := referencedSiblings(s, e.Rewrite)
		sort.Strings(refs)
		for _, ref := range refs {
			switch {
			case o.dangerous && e.Rewrite.Allowed[ref]:
				// approved
			case o.useCache:
				// snapshotted
			default:
				fmt.Fprintf(os.Stderr, "WARNING: session references sibling %s (outside sandbox) but neither --cache nor --dangerous is set; those actions will fail\n", ref)
			}
		}
	}

	// Compressed filesystem/repository caching: restore a snapshot keyed by the
	// session before replay so prompts that need pre-existing data get it.
	var c *cache.Cache
	if o.useCache {
		c, err = cache.New(o.cacheDir)
		if err != nil {
			return err
		}
		key := cache.Key(s.ID, o.dir, initialPrompt(s))
		if c.Exists(key) {
			fmt.Printf("restoring cached state for %s\n", key)
			if !o.dryRun {
				if err := c.Restore(key, o.dir); err != nil {
					return err
				}
			}
		} else {
			fmt.Printf("no cache entry for %s; will snapshot after replay\n", key)
		}
		e.SaveCache = func(tag string) error {
			return c.Save(key, o.dir, true)
		}
	}

	rep, err := e.Replay(s)
	if err != nil {
		return err
	}

	if o.useCache && !o.dryRun && c != nil {
		key := cache.Key(s.ID, o.dir, initialPrompt(s))
		if err := c.Save(key, o.dir, true); err != nil {
			fmt.Fprintf(os.Stderr, "cache save failed: %v\n", err)
		} else {
			fmt.Printf("snapshot saved to cache key %s\n", key)
		}
	}

	ran, failed, skipped := rep.Counts()
	fmt.Printf("\nreplay complete: %d ran, %d failed, %d skipped (source: %s)\n", ran, failed, skipped, src)
	for _, f := range rep.Failures() {
		fmt.Printf("  FAIL [%s] %s: %s\n", f.Kind, f.Summary, f.Error)
	}
	return nil
}

func initialPrompt(s *session.Session) string {
	for _, m := range s.Messages {
		if m.Role != "user" {
			continue
		}
		for _, p := range m.Parts {
			if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
				return p.Text
			}
		}
	}
	return s.Title
}

func runList(o opts) error {
	ld, err := session.Open(o.db)
	if err != nil {
		return err
	}
	defer ld.Close()
	sessions, err := ld.List(o.limit)
	if err != nil {
		return err
	}
	fmt.Printf("%-26s %-20s %s\n", "SESSION ID", "NAME", "TITLE")
	for _, s := range sessions {
		title := strings.TrimSpace(s.Title)
		if len(title) > 60 {
			title = title[:60] + "..."
		}
		fmt.Printf("%-26s %-20s %s\n", s.ID, s.Slug, title)
	}
	fmt.Printf("\n%d sessions\n", len(sessions))
	return nil
}

func runCache(sub string, rest []string, o opts) error {
	c, err := cache.New(o.cacheDir)
	if err != nil {
		return err
	}
	switch sub {
	case "save":
		if len(rest) < 2 {
			return fmt.Errorf("cache save requires <key> <dir>")
		}
		key, dir := rest[0], rest[1]
		fmt.Printf("saving %s -> %s/%s.tar.gz\n", dir, o.cacheDir, key)
		return c.Save(key, dir, true)
	case "restore":
		if len(rest) < 2 {
			return fmt.Errorf("cache restore requires <key> <dir>")
		}
		key, dir := rest[0], rest[1]
		fmt.Printf("restoring %s/%s.tar.gz -> %s\n", o.cacheDir, key, dir)
		return c.Restore(key, dir)
	case "list":
		entries, err := c.List()
		if err != nil {
			return err
		}
		for _, e := range entries {
			fmt.Printf("%s  %8d bytes  %s\n", e.Key, e.Size, e.Modified.Format("2006-01-02 15:04"))
		}
		fmt.Printf("\n%d entries\n", len(entries))
		return nil
	case "info":
		if len(rest) < 1 {
			return fmt.Errorf("cache info requires <key>")
		}
		e, err := c.Info(rest[0])
		if err != nil {
			return err
		}
		fmt.Printf("key:     %s\nsize:    %d bytes\nmodified: %s\n", e.Key, e.Size, e.Modified.Format(timeFmt))
		return nil
	default:
		return fmt.Errorf("unknown cache subcommand %q", sub)
	}
}

const timeFmt = "2006-01-02 15:04"
