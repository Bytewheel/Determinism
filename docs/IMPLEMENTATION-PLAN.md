# Determinism — Implementation Plan

Determinism ingests an opencode AI session and deterministically replays every
command and file operation (write, edit, delete, overwrite) the session
performed. It supports two input sources and several execution modes.

## 1. Goals

- Reproduce, in order, all `bash` commands and all file mutations
  (`write`, `edit`, `apply_patch`) an AI session performed.
- Support the **opencode chat text format** (markdown transcripts) and
  **opencode database reads** from `opencode.db`, with sessions looked up by
  **session name** (slug), id, title, or **initial prompt**.
- Execute **subprompts** (task/subagent child sessions) **gracefully**: fail
  softly and continue if anything inside them fails.
- Execute subprompts from the **prompts that lead up to them** (replaying the
  parent's leading commands/writes first), failing gracefully if any leading
  command or write fails.
- Provide **compressed filesystem/repository caching** behind its own flag so
  prompts that need pre-existing data can be replayed deterministically.

## 2. Scope

| In scope                                     | Out of scope (recorded + skipped)   |
|----------------------------------------------|-------------------------------------|
| bash tool replay                             | read-only tools (read, glob, grep)  |
| write tool replay (create/overwrite)         | webfetch, websearch                 |
| edit tool replay (old->new replace)          | question, todowrite, skill          |
| apply_patch replay (add/update/delete/move)  | summarize, codesearch, rewind       |
| task/subtask subprompt replay (opt-in)       |                                   |
| chat-text parsing                            |                                   |
| SQLite session lookup + load                 |                                   |
| compressed fs/repo cache (opt-in)            |                                   |

## 3. Input sources

### 3.1 Chat text format
The markdown transcript produced by the TUI export. Format
(`packages/tui/src/util/transcript.ts`):

```
# {title}

**Session ID:** {id}
**Created:** ...
**Updated:** ...

---

## User

{text}

---

## Assistant (agent · model · duration)

{text}

**Tool: bash**

**Input:**
```json
{...}
```

**Output:**
```
...
```

---
```

Parsed by `internal/session/chat.go`. Tool inputs/outputs are captured in JSON
fenced blocks; `ParseToolPart` normalizes input/output order.

### 3.2 opencode database
SQLite at `~/.local/share/opencode/opencode.db` (read-only). Tables:
- `session(id, slug, directory, title, parent_id, time_created, time_updated)`
- `message(id, session_id, time_created, data JSON)`
- `part(id, message_id, session_id, time_created, data JSON)`

Part JSON: `{type:"tool", tool:"bash", state:{status,input,output,error}}`,
`{type:"text", text}`, `{type:"reasoning", reasoning}`, `{type:"subtask", prompt,...}`.

Lookup priority: id (`ses_...`) → slug (session name) → title substring →
initial user prompt substring. See `internal/session/db.go`.

## 4. Architecture

```
cmd/determinism       CLI (run, plan, list, cache save/restore/list/info)
internal/session      types + chat parser + DB loader + lookup
internal/replay       engine, tool executors, apply_patch parser, subprompts
internal/cache        compressed gzip-tar filesystem/repo cache
internal/plan         implementation-plan markdown generator
```

Data flow: `reference → loader → session.Session → engine.Replay → Report`.

## 5. Execution model

- Replay is strictly ordered by `time_created` (message then part), so the
  state leading up to each action exists before it runs.
- **Graceful failure (default):** every action is attempted; failures are
  collected in the `Report` and replay continues. `--strict` aborts on first
  failure.
- **bash:** run with `/bin/sh -c` (or `--shell`), cwd resolved from
  `workdir`/session directory, honoring `timeout` (ms). Non-zero exit → failure.
- **write:** `mkdir -p` + write (overwrite). **edit:** single or
  `replaceAll` substitution; "oldString not found" → failure.
- **apply_patch:** parse `*** Begin Patch`/`*** End Patch` with add/update/
  delete/move hunks and apply to the working tree (see §7).
- **subprompts:** see §6.

## 6. Subprompt execution (graceful, behind flag)

Requirement ladder, implemented in `internal/replay/subprompt.go`:

1. **Baseline (no flag):** `task` tool parts and `subtask` parts are recorded
   as *skipped*; nothing is executed. This is the default.
2. **`--subprompts`:** subprompts are executed. Child session resolution:
   a. `task_id: ses_...` parsed from the task tool output (completed tasks), or
   b. `session.parent_id` + title/description match (`Loader.FindChild`) for
      tasks whose output recorded no task_id (e.g. errored tasks).
3. **Leading-up replay:** because replay is linear, every command and
   read/write from the prompts leading up to the subprompt has already run in
   the parent before the subprompt executes, so the child inherits that state.
4. **Graceful failure of the lead-up:** if any earlier command/write in the
   parent failed, the subprompt is still attempted; if the child itself fails,
   the failure is recorded and replay continues. `--strict` upgrades this.

## 7. apply_patch format

From `packages/opencode/src/patch/index.ts`:

```
*** Begin Patch
*** Add File: <path>
+content lines (each prefixed +)

*** Update File: <path>
*** Move to: <newpath>           (optional)
@@
 context
-old
+new

*** Delete File: <path>
*** End Patch
```

Chunks: lines prefixed ` ` (context), `-` (old), `+` (new); `@@` starts a
context chunk; `*** End of File` appends at EOF. `internal/replay/patch.go`
implements parsing and application.

## 8. Compressed filesystem/repository cache (behind its own flag)

`internal/cache/cache.go`:

- Snapshot = gzip-compressed tar of a directory tree.
- `Key(sessionID, workdir, prompt)` → sha256, 32 hex chars.
- **`cache save <key> <dir>`** / **`cache restore <key> <dir>`** /
  **`cache list`** / **`cache info <key>`** subcommands.
- In `run --cache`, before replay the engine restores the snapshot keyed by
  `(session, workdir, initial prompt)` so prompts requiring pre-existing data
  start from that state; after replay the resulting state is snapshotted.
- Skips `.git` and the cache dir itself; symlinks are skipped; unsafe archive
  paths (absolute / `..`) are rejected on restore.

## 9. CLI

```
determinism run <session|name|prompt|file.md> [--dir P] [--dry-run]
                 [--subprompts] [--cache] [--cache-dir P] [--strict]
                 [--db P] [--shell S]
determinism plan <session|...> [--out file.md]      # write implementation plan
determinism list [--limit n]
determinism cache save|restore|list|info ...
```

## 10. Milestones

- M1 — session types, chat parser, DB loader, lookup (name/title/prompt).
- M2 — replay engine: bash/write/edit/apply_patch + graceful failure + Report.
- M3 — subprompt resolution + execution behind `--subprompts`.
- M4 — compressed cache behind `--cache`.
- M5 — CLI, plan generator, tests, README.

See `ROADMAP.md` for status.
