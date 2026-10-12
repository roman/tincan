# Tincan and Concierge

Tincan provides durable messages and notifications between agents. Concierge
provides local task intake, worker bindings, result handling, and supervision.
Install both commands to run a supervisor from a project root. Repository
skills and lifecycle hooks supply the session instructions.

| Command | Responsibility |
| --- | --- |
| `tincan inbox` | Send, list, wait for, read, and acknowledge messages. |
| `tincan notify` | Prompt a Herdr endpoint while its inbox has pending messages. |
| `concierge state` | Initialize fresh task state, back up legacy state, migrate, and recover. |
| `concierge backlog` | Queue work, manage holds, dispatch, accept results, and retire worker bindings. |
| `concierge task` | Adopt workers, exchange bound assignments and replies, stop panes, and inspect tasks. |
| `concierge worker` | Inspect recorded worker identities and recovery markers. |
| `concierge primary` | Claim, verify, and release root ownership from Linux lifecycle hooks. |
| `concierge control` | Inspect, start, and interrupt live Herdr agents. |
| `concierge supervise` | Monitor a manual pane or supervise a task with inbox delivery and worker recovery. |
| `concierge brief` | Show pending primary messages, queued work, workers, and unfinished completions. |

## Install and verify

```sh
nix profile install .#tincan .#concierge
tincan --version
concierge --version
concierge --help
```

The primary package outputs each expose one public executable: `tincan` and
`concierge`. Both include pinned Herdr 0.9.3. Concierge also supplies process
inspection through Linux `procps` or native `/bin/ps` on macOS. Primary ownership
requires Linux `/proc`. Herdr is the supported terminal adapter. The commands
can use existing panes; they do not create terminal layouts.

```sh
nix build .#tincan .#concierge
nix develop
nix fmt
go test ./...
nix flake check --system x86_64-linux
```

The development shell provides Go 1.25, golines, and Herdr. `nix fmt` formats Go
at 100 columns. The Linux flake gate builds command packages, checks formatting,
runs the Go suite, and tests installed commands with an empty caller `PATH`.
A dependency check keeps Concierge task packages out of the generic Tincan
binary. Individual standalone package outputs remain available while consumers
complete their command migration.

## State roots

From the project root, set absolute paths once:

```sh
export CONCIERGE_HOME="$PWD/.concierge"
export TINCAN_HOME="$PWD/.tincan"
concierge state init
concierge
```

`--root` overrides `CONCIERGE_HOME` for Concierge and `TINCAN_HOME` for Tincan.
Concierge's `--message-root` overrides `TINCAN_HOME`. Without these settings,
the commands use `.concierge` and `.tincan` under the current directory.
Concierge requires clean absolute state paths. Its primary inbox recipient is
`concierge`; use `--recipient NAME` consistently when selecting another primary.

`state init` requires an existing parent directory. It initializes a fresh root
or confirms an existing version 2 root. It preserves primary ownership files,
the installed `profile` symlink, and existing version 2 task records. It rejects legacy
backlogs, unknown fresh-root entries, pending migrations, and unsafe file types.
Stop state writers and installers during fresh initialization.

Task records under `.concierge/records/` are authoritative. `backlog.md` is a
readable projection rebuilt by task operations. Edit tasks through the command.
A failed projection can leave a committed task change; inspect the task and
retry the same request after correcting the storage error.

### Migrate existing task state

Stop all legacy task writers and controllers first. Keep them stopped until
all task callers use the version 2 commands. Choose an absolute backup path
beside the state root:

```sh
concierge state backup --backup /absolute/concierge-state-backup
concierge state migrate --backup /absolute/concierge-state-backup
```

`backup` refuses an existing destination. `migrate` creates a backup or verifies
and reuses an existing one. It converts the legacy records and publishes
version 2 after validation. An interrupted migration blocks task operations
until recovery verifies the backup and converted records:

```sh
concierge state recover --backup /absolute/concierge-state-backup
```

Recovery completes a pending switch. It does not restore an arbitrary backup.

## Independent messages

```sh
tincan inbox send worker-1 --key request-1 --body 'Please check the task.'
tincan inbox list worker-1
tincan inbox read worker-1 MESSAGE_ID
tincan inbox ack worker-1 MESSAGE_ID
tincan inbox wait concierge --timeout 30s
```

A stable key makes an identical send retry return the same message. Reusing the
key with another body returns an error. Use `--body-file FILE` or
`--body-file -` to read a body from a file or stdin. Acknowledge a message after
handling it. A wait returns pending messages or a bounded checkpoint.

```sh
tincan notify --recipient concierge --target PRIMARY_PANE --session work
```

The notifier prompts the selected Herdr endpoint while messages remain pending.
A prompt directs the agent to its inbox. Acknowledgement records that the
recipient handled the message. Notification runs until interruption. Its
interval defaults to five seconds. Use `--herdr PATH` to select another Herdr
executable.

## Local task lifecycle

Queue a task and read its full contents:

```sh
concierge backlog add task-1 'Fix startup' --kind task --repo exmachina --body-file brief.txt
concierge backlog list
concierge backlog show task-1 --full
```

An identical intake retry can repair a failed backlog projection. A changed
request for the same task ID returns a conflict. Repeat `--blocked-by ID` to
record dependencies. Use `backlog hold task-1 --reason TEXT` and
`backlog release task-1` to control dispatch.

Choose a fresh random generation before starting the idle worker. Supply it as
`CONCIERGE_SPAWN_GENERATION` through the harness launch settings. Adopt its verified
pane with that same generation and the exact Herdr session, name, project,
worktree, and reusable resume arguments:

```sh
concierge task adopt task-1 --generation GEN --name worker-1 --session work --pane PANE_ID \
  --project /absolute/project --worktree /absolute/worktree \
  --agent-arg resume --agent-arg THREAD_ID
concierge worker show task-1
concierge backlog start task-1
```

Adoption verifies the live endpoint and records the supplied generation. It does
not inspect the worker environment or start task dispatch. The start command
checks holds and dependencies. The worker uses its launch generation for its
assignment and reply commands. Keep generation overrides out of the reusable
resume arguments; a controlled restart supplies its own reserved generation.

```sh
concierge task send task-1 --key assignment-1 --body-file brief.txt
concierge task receive task-1 MESSAGE_ID --generation GEN
concierge task reply send task-1 --generation GEN --key answer-1 \
  --assignment-id MESSAGE_ID --body-file reply.txt
```

`task send` stores a generation-bound assignment before notifying the worker.
A failed notification leaves the assignment durable. Retry the same keyed
request after correcting the worker or notification error. The worker acts on
an assignment only when `receive` reports `status: current`. Stale assignments
have no actionable body. For work that needs no reply, use
`task ack task-1 MESSAGE_ID --generation GEN` after handling the assignment.

The reply command publishes the result before acknowledging its assignment.
An identical keyed retry completes interrupted acknowledgement. The primary
checks a result without accepting it, then resolves a bound reply:

```sh
tincan inbox list concierge
concierge task reply check REPLY_ID
concierge backlog resolve REPLY_ID --mode close --closed-on YYYY-MM-DD
concierge task stop task-1 --generation GEN
concierge backlog cleanup task-1 --generation GEN
```

Act on a reply only when `check` reports `status: current`. Resolution verifies
its assignment and worker generation, records acceptance, and acknowledges the
reply. Close requires an explicit valid closing date. Retry the same reply ID,
mode, and date after interruption. For a held task that must return to the
queue, use `--mode retain` without a closing date. Finish the retained worker's
cleanup before releasing its hold:

```sh
concierge backlog resolve REPLY_ID --mode retain
concierge task stop task-1 --generation GEN
concierge backlog cleanup task-1 --generation GEN
concierge backlog release task-1
```

For a result without a bound reply, use `backlog finish task-1 --generation GEN
--mode close --closed-on YYYY-MM-DD`. The equivalent Retain request uses
`--mode retain` without a date, followed by stop, cleanup, and release.

For an active accepted generation, stop closes its exact recorded pane. A retry
for a retired generation can succeed from its receipt and leaves a newer worker
unchanged. Cleanup verifies pane departure and retires the accepted worker
binding. Preserve the accepted generation through these
steps. `task pending` lists unfinished completion work. `task cancel` releases
an unbound adoption reservation; it rejects confirmed bindings.

## Supervision and root hooks

```sh
concierge control --session work status worker-1
concierge control --session work interrupt worker-1
concierge control --session work start --pane PANE_ID --name worker-1 --kind codex
concierge supervise task task-1
concierge supervise pane --pane PANE_ID --name worker-1 --kind codex \
  --agent-arg resume --agent-arg THREAD_ID
```

Task supervision holds one controller lease per task. It checks the recorded
session, pane, and live worktree. It delivers pending inbox prompts and recovers
confirmed missing workers with their exact launch arguments. Recovery records
a new generation and a durable bootstrap message. A registered agent with no
live process can require Herdr-side repair before a restart succeeds.

Manual pane supervision uses ambient Herdr routing and the supplied launch
arguments. Task supervision uses the recorded session. Neither supervisor
creates a pane. Use `--interval`, `--restart-tolerance`, and `--restart-window`
on the `supervise` group to configure checks. Both modes run until interrupted.
Restart arguments must include the agent's resume identity when work needs to
continue after a restart.

Lifecycle hooks can call `primary claim`, `primary verify`, and `primary release`
with hook JSON on stdin. These require SessionStart, Stop, and SessionEnd events,
respectively. The hook's working directory must equal the parent of the
Concierge state root. One live session owns the primary role. Repository skills
instruct secondary sessions to leave shared task supervision to that owner.
The default `concierge` command renders the root brief after state setup.
