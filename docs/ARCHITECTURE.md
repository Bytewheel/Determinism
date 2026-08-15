# Determinism — Architecture

## Overview

Determinism is a Go CLI that ingests an opencode AI session and replays it
deterministically: every `bash` command is re-run and every `write`, `edit`,
and `apply_patch` file mutation is applied to the filesystem in the same order
the model performed them.

## Package layout

```
cmd/determinism/main.go     CLI entry point, flag handling, subcommands
internal/session/           source-agnostic session model + loaders
  types.go                  Session / Message / Part / Lookup
  chat.go                   chat-text markdown parser
  db.go                     SQLite loader (read-only), lookup, children
internal/replay/            execution engine
  engine.go                 ordered replay + report
  input.go                  tool input decoders
  patch.go                  apply_patch parser/applier
  subprompt.go              graceful subprompt execution
internal/cache/             compressed fs/repo cache (gzip tar)
internal/plan/              implementation-plan markdown generator
```

## Core types

```go
type Session struct {
    ID, Slug, Title, Directory, ParentID string
    TimeCreated, TimeUpdated time.Time
    Messages []Message
}
type Message struct {
    ID, SessionID, Role string
    TimeCreated time.Time
    Parts []Part
}
type Part struct {
    ID, MessageID, Type, ToolName, Text string
    Input, Output []byte          // raw JSON / raw output
    Status string                 // pending|running|completed|error
    ChildID string                // child session for task/subtask
    Prompt, Description, Agent string
}
type Report struct {
    Session *Session
    Outcomes []Outcome            // one per part, in replay order
}
type Outcome struct {
    SessionID, MessageID, PartID, Kind, Summary, Error string
    OK bool
}
```

## Engine flow

1. Normalize to a `session.Session` (from chat text or DB).
2. Iterate messages (by `TimeCreated`), then parts within each message.
3. Dispatch per part type:
   - `text` / `reasoning` → recorded, not executed.
   - `tool` → switch on `ToolName`:
     - `bash`, `write`, `edit`, `apply_patch` → executor.
     - `task` → subprompt path (skip unless `--subprompts`).
   - `subtask` → subprompt path.
   - anything else → recorded as skipped.
4. Failures are collected into the `Report`; replay continues by default
   (`--strict` aborts).

## Path resolution and sandboxing

`Engine.resolvePath` is the single choke point for every path a tool touches
(bash `workdir`, write/edit targets, apply_patch add/update/delete/move):

- Default: relative paths join `Engine.WorkDir`; absolute paths are honored
  as recorded.
- Sandboxed (always on for `run`): a `Rewriter` maps relative paths under the
  sandbox mirror (`TargetDir`), mirrors absolute paths at/under the session
  directory into the sandbox, and rejects out-of-session paths and `..`
  traversal. Replays therefore never write outside the sandbox. Bash command
  *text* is rewritten too (`Rewriter.RewriteCommand`): absolute paths inside
  commands, redirect/assignment targets, and escaping-relative sibling refs
  (`../princess-vpn/x`) are remapped; system paths (`/dev/null`) and heredoc
  bodies are preserved.
- The session's files are copied into the sandbox on first access
  (copy-on-first-access, skipped for dry runs and `.git`/build dirs) so replay
  can read the original source without touching it. Real paths are never
  written.
- Sibling repos (outside the session dir): default reject; `--cache` extracts
  a gzip-tar snapshot under the sandbox (`<sandbox>/.determinism/siblings/`),
  created from the real path if absent; `--dangerous` resolves to the real
  path after per-sibling approval; `--dangerous --cache` keeps the snapshot in
  real-time sync (`Rewriter.SyncSiblings`).

## Subprompt resolution

`Loader.Children(parentID)` lists child sessions. A task part's child is
resolved by, in order:
1. `task_id: ses_...` extracted from the task tool output;
2. `Loader.FindChild(parentID, description)` — title substring match on
   children when no task_id was recorded (e.g. errored tasks).

## Caching

Cache key = sha256(sessionID, workdir, initial prompt). Snapshots are gzip
tars. In `run --cache`, the snapshot is restored before replay (if present)
and re-saved after; `cache save/restore/list/info` operate on keys directly.

## Error handling

- Every executor returns an `Outcome`; never panics.
- Graceful failure means: record error, continue replaying remaining parts.
- Subprompt failures are downgraded to reported failures, never fatal.
