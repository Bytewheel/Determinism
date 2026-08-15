# Determinism — Roadmap

## Milestones

### M1 — Ingestion (DONE)
- [x] `internal/session` types (Session/Message/Part/Lookup).
- [x] Chat-text markdown parser (`internal/session/chat.go`).
- [x] Read-only SQLite loader + session lookup by id / name (slug) / title /
      initial prompt (`internal/session/db.go`).
- [x] Child session enumeration (`Children`) + description/title matching
      (`FindChild`).

### M2 — Replay engine (DONE)
- [x] Ordered replay of bash / write / edit / apply_patch.
- [x] Graceful failure collection into `Report`; `--strict` abort.
- [x] apply_patch parser + applier (add/update/delete/move, EOF append).
- [x] Tool input decoders tolerant of `filePath` vs `path`.

### M3 — Subprompts (DONE)
- [x] Subprompts skipped by default (recorded, non-fatal).
- [x] `--subprompts` executes task/subtask child sessions.
- [x] Child resolution from `task_id` output or parent_id+title match.
- [x] Leading-up commands/writes naturally replayed (linear ordering), child
      inherits parent state.
- [x] Graceful failure: failing child reported, replay continues.

### M4 — Compressed filesystem/repository cache (DONE)
- [x] gzip-tar snapshots keyed by (session, workdir, initial prompt).
- [x] `cache save|restore|list|info` subcommands.
- [x] `--cache` integration: restore-before / save-after replay.
- [x] Safety: `.git` and cache-dir excluded; symlinks skipped; absolute/`..`
      paths rejected on restore.

### M5 — CLI & docs (DONE)
- [x] `run`, `plan`, `list`, `cache` subcommands; interspersed flags.
- [x] Implementation planning docs (this tree).
- [x] Unit tests: chat parser, apply_patch, DB loader (skip if absent).

### M6 — Sandboxing (DONE)
- [x] `Rewriter` maps relative + session-absolute paths into a sandbox dir;
      out-of-session and traversal paths rejected.
- [x] Engine routes write/edit/apply_patch/bash-workdir through the rewriter.
- [x] CLI `--rewrite-dir` flag; workdir becomes the sandbox root.
- [x] Unit tests (`rewrite_test.go`, `sandbox_test.go`): writes land in the
      sandbox, original tree untouched, escapes fail gracefully.
- [x] Every `run` is sandboxed by default (`~/.local/share/determinism/<name>`).
- [x] Bash command *text* is rewritten (absolute paths, redirect/assignment
      targets, escaping-relative sibling refs); heredocs and system paths
      preserved.
- [x] Copy-on-first-access: session files mirrored into the sandbox so replay
      can read source without touching the original tree.
- [x] Sibling repos (`--cache` snapshots, `--dangerous` real-path access with
      approval, `--allow-dir` pre-approval, real-time sync when combined).
- [x] Sibling/command tests (`siblings_test.go`); dry-run shows sandboxed
      command lines.

## Future work

- [ ] Verify against large sessions end-to-end (e.g. `swift-pixel`) without
      `--dry-run`.
- [ ] `apply_patch` multi-hunk update with multiple `@@` context chunks
      (currently collapses to one old→new sequence per hunk).
- [ ] Windows `cmd` shell replay (currently POSIX `sh -c`).
- [ ] Structured JSON report output (`--json`).
- [ ] Track shell `cwd` across consecutive bash commands so escaping-relative
      sibling refs after `cd` rewrite exactly.
- [ ] Subprompt depth limit / cycle guard.
