# Determinism

Deterministically replay an opencode AI session: every command and file write,
edit, delete, or overwrite the session performed, in the same order.

## Usage

```
determinism run <session|name|prompt|file.md> [flags]
determinism plan <session|name|prompt|file.md> [--out plan.md]
determinism list [--limit n]
determinism cache save <key> <dir>
determinism cache restore <key> <dir>
determinism cache list
```

A session reference can be an opencode session id (`ses_...`), a session name
(slug, e.g. `swift-pixel`), a title substring, the initial prompt text, or a
path to a chat-text transcript (`.md`).

## Examples

```sh
# Dry-run a session by name (sandboxed by default, nothing is written)
determinism run swift-pixel --dry-run

# Sandbox replay: session paths are remapped into a sandbox so nothing touches
# the original tree. Every run is sandboxed; --rewrite-dir / --dir choose where.
determinism run swift-pixel --rewrite-dir /tmp/work

# Sandbox location is configurable; default is ~/.local/share/determinism/<name>
determinism run swift-pixel

# Replay into a scratch directory, executing subprompts too
determinism run swift-pixel --dir /tmp/work --subprompts

# Look up and plan a session from its initial prompt
determinism plan "rewrite yargen to use yara-x"

# Enable compressed filesystem/repository caching
determinism run eager-rocket --cache --cache-dir ~/.cache/determinism

# Allow a sibling repository (outside the session dir) via its real path,
# with a one-time approval prompt per sibling (or --allow-dir to pre-approve)
determinism run dvpn-session --dangerous
determinism run dvpn-session --dangerous --allow-dir ~/Documents/hawksnestsoftware/princess-vpn
```

## Flags

| Flag           | Meaning                                                    |
|----------------|------------------------------------------------------------|
| `--db path`    | opencode database (default `~/.local/share/opencode/opencode.db`) |
| `--dir path`   | working directory for replay (default: session directory)  |
| `--rewrite-dir path` | sandbox: remap session's absolute paths under this dir (default `~/.local/share/determinism/<name>`) |
| `--dry-run`    | print actions without executing them                       |
| `--subprompts` | execute subprompts (task/subagent sessions)                |
| `--cache`      | snapshot sibling repositories into the cache; sibling refs resolve to the extracted snapshot |
| `--cache-dir path` | cache directory (default `~/.cache/determinism`)       |
| `--dangerous`  | allow real-path access to sibling repos (after per-sibling approval) |
| `--allow-dir path` | pre-approve a sibling repo for `--dangerous` (repeatable) |
| `--strict`     | stop at first failure instead of failing gracefully        |
| `--shell path` | shell for bash replay (default `/bin/sh`)                  |
| `--out file`   | write a plan to a file (`plan`)                            |

## How it works

- **Ingestion:** chat-text transcript or the opencode SQLite DB (read-only).
- **Replay:** ordered `bash`, `write`, `edit`, `apply_patch`, and (opt-in)
  `task`/`subtask` subprompt sessions.
- **Sandboxing:** every run replays into a sandbox (default
  `~/.local/share/determinism/<name>`). Session-absolute paths are remapped
  into the sandbox; the session's files are copied into the sandbox on first
  access (copy-on-first-access) so replay can read source; bash command *text*
  is rewritten so commands execute against the sandbox. Real trees are never
  written.
- **Sibling repos:** repos outside the session dir (e.g. `../princess-vpn`)
  are rejected by default; `--cache` snapshots them into the sandbox,
  `--dangerous` allows real-path access after approval (`--allow-dir`
  pre-approves). With both, cache is kept in sync in real time.
- **Graceful failure:** errors are collected and replay continues; `--strict`
  aborts.
- **Subprompts:** child sessions run with the parent's leading-up state;
  failures are reported, never fatal.
- **Cache:** gzip-tar snapshots of a workdir keyed by session+workdir+prompt,
  restored before replay so prompts that need pre-existing data work.

## Build

```sh
go build -o bin/determinism ./cmd/determinism
```

## Docs

- `docs/IMPLEMENTATION-PLAN.md` — master plan
- `docs/FORMAT-SPEC.md` — chat-text, DB, patch, and cache formats
- `docs/ARCHITECTURE.md` — component design
- `docs/ROADMAP.md` — milestones and status
