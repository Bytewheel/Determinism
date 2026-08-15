// Command determinism ingests an opencode AI session and deterministically
// replays every command and file operation (write, edit, delete, overwrite)
// the session performed.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
  --dry-run          print actions without executing them
  --subprompts       execute subprompts (task/subagent sessions)
  --cache            enable compressed filesystem/repository caching
  --cache-dir path   cache directory (default ~/.cache/determinism)
  --strict           stop at the first failure instead of failing gracefully
  --shell path       shell used for bash replay (default /bin/sh)
  --out file         write plan to a file (plan command)
`)
}

type opts struct {
	db         string
	dir        string
	dryRun     bool
	subprompts bool
	useCache   bool
	cacheDir   string
	strict     bool
	shell      string
	out        string
	limit      int
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
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.BoolVar(&o.subprompts, "subprompts", false, "")
	fs.BoolVar(&o.useCache, "cache", false, "")
	fs.StringVar(&o.cacheDir, "cache-dir", "", "")
	fs.BoolVar(&o.strict, "strict", false, "")
	fs.StringVar(&o.shell, "shell", "", "")
	fs.StringVar(&o.out, "out", "", "")
	fs.IntVar(&o.limit, "limit", 50, "")
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

func newEngine(o opts, ld *session.Loader, s *session.Session) *replay.Engine {
	e := &replay.Engine{
		WorkDir:        o.dir,
		Shell:          o.shell,
		Subprompts:     o.subprompts,
		FailGracefully: !o.strict,
		DryRun:         o.dryRun,
		Log:            func(f string, a ...any) { fmt.Fprintf(os.Stdout, f+"\n", a...) },
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
	return e
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

func runReplay(ref string, o opts) error {
	s, src, err := loadSession(ref, o)
	if err != nil {
		return err
	}
	if o.dir == "" {
		o.dir = s.Directory
	}
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

	e := newEngine(o, ld, s)

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
