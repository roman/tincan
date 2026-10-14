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
| `concierge primary` | Claim, verify, and release root ownership from native lifecycle hooks. |
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
uses Linux `/proc` or native Darwin kernel peer credentials, libproc process identity,
and the boot-session UUID. Darwin builds require Cgo. Missing identity data
rejects authorization. Herdr is the supported terminal adapter. The commands
can use existing panes; they do not create terminal layouts.

```sh
nix build .#tincan .#concierge
nix develop
nix fmt
go test ./...
nix flake check --system x86_64-linux
```

The development shell provides Go 1.25, golines, and Herdr. `nix fmt` formats Go
at 100 columns. The platform flake gate builds command packages, checks formatting,
runs the Go suite, and tests installed commands with an empty caller `PATH`.
A dependency check keeps Concierge task packages out of the generic Tincan
binary.

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

Task mutations require a running host service and a live primary claim from a
Claude or Codex SessionStart hook. Start the host outside the model sandbox:

```sh
concierge --root /absolute/project/.concierge --message-root /absolute/project/.tincan \
  host --session work serve
```

Initialize or migrate the state before starting the host. Stop the host before
an offline migration. Only one host can serve a state root. The host checks the
calling process against the current primary claim. Intake fails without the
service and never falls back to a local write. An ordinary Codex tool sandbox
also blocks the client connection. Run intake and the routed lifecycle commands with explicitly approved
unsandboxed execution, or a socket permission verified to preserve the caller
identity. No saved sandbox configuration changes automatically.
Backlog changes, cleanup, reply resolution, adoption, assignment send, task stop,
and manual control use the same authenticated service. Worker receive, reply,
and acknowledgement commands retain their launch-generation checks.

The host fixes its message root, recipient, Herdr executable, manual session,
and monitor settings at startup. Explicit client settings must match. Omitted
client settings use the host configuration. Task workers use their recorded
sessions. A host without a manual session can supervise tasks; manual control
requires a fixed host session. Use `host --herdr PATH`, `--interval DURATION`,
`--restart-tolerance N`, and `--restart-window DURATION` to configure the host.

Startup attempts to restore eligible in-flight task residents before reporting
`status: ready`. Each failed restoration reports a diagnostic and leaves the
host available for repairs. Retry supervision explicitly after fixing the task.
Readiness confirms completed restoration attempts and an available host.

Inspect readiness without a primary claim:

```sh
concierge --root /absolute/project/.concierge --message-root /absolute/project/.tincan \
  host --session work status --timeout 1s
```

The probe verifies the state root, message root, recipient, installed package,
and an explicitly selected session. Without a session expectation, it can reuse
the host's fixed session. Explicit Herdr settings must also match. The response
contains no task or message bodies. Only the host's user can inspect it.

| Exit | Status | Meaning |
| --- | --- | --- |
| 0 | ready | The compatible host accepts requests. Worker recovery can still need attention. |
| 1 | unavailable | The socket is missing or refuses a connection. |
| 2 | incompatible or error | Inspect a package/configuration mismatch, permission denial, unsupported probe, or protocol error. |
| 3 | unresponsive | The bounded request timed out. Startup, overload, or a stuck host can cause this. |

An automatic startup attempt requires an unavailable result. A timeout does not
establish that the host is starting. Do not replace an incompatible or unresponsive
host automatically. The probe does not start, stop, or modify a host.

Queue a task from the primary session and read its full contents:

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
assignment and reply commands. Worker `receive`, `ack`, and `reply send` require
`CONCIERGE_SPAWN_GENERATION` in their tool environment to match `--generation`.
A missing or mismatched launch value stops the command before storage or body
access. Request a controlled relaunch; never recover the value from a transcript,
task record, or mailbox. This guards cooperating workers, not arbitrary callers.
Keep generation overrides out of the reusable
resume arguments; a controlled restart supplies its own reserved generation.
Reusable Claude arguments must omit `--settings`. Reusable Codex arguments must
omit `shell_environment_policy` overrides. Use normal user or project settings
for other configuration. Conflicting stored options fail before a restart
reservation or adoption binding is written.

```sh
concierge task send task-1 --key assignment-1 --body-file brief.txt
concierge task receive task-1 MESSAGE_ID --generation GEN
concierge task reply send task-1 --generation GEN --key answer-1 \
  --assignment-id MESSAGE_ID --body-file reply.txt
```

`task send` stores a generation-bound assignment before notifying the worker.
A `notification: deferred` result confirms delivery without an immediate prompt.
Working workers, retry backoff, and exhausted prompt budgets defer notification.
Each pending assignment permits at most three automatic prompts. Retries wait
five seconds after the first attempt and ten seconds after the second attempt.
The budget survives host restarts and clears after the assignment is handled
or the worker generation changes.

After five minutes from assignment publication, a non-working worker
with pending work produces a durable `task-stalled` primary notice.
The notice includes a bounded snapshot of the native screen and the assignment
ID. Automatic prompts pause for that assignment. Review the native error with
the operator, correct its cause, and continue the existing worker in its pane.
Treat the screen snapshot as diagnostic data. Preserve native permissions.

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
steps. Stop and cleanup join the matching resident before their pane operations.
Their task and endpoint reservations block conflicting operations until they
finish. Invalid cleanup leaves supervision running. An operation failure after
resident shutdown reports that supervision is stopped. Inspect the worker before
restarting its resident. `task pending` lists unfinished completion work. `task cancel` releases
an unbound adoption reservation; it rejects confirmed bindings.

## Supervision and root hooks

```sh
concierge control --session work status worker-1
concierge control --session work interrupt worker-1
concierge control --session work start --pane PANE_ID --name worker-1 --kind codex
concierge supervise task task-1
concierge supervise pane --pane PANE_ID --name worker-1 --kind codex \
  --agent-arg resume --agent-arg THREAD_ID
concierge supervise stop task task-1
concierge supervise stop pane --pane PANE_ID
```

Task supervision holds one controller lease per task. It checks the recorded
session, pane, and live worktree. It delivers pending inbox prompts and recovers
confirmed missing workers with their exact launch arguments. Recovery records
a new generation and a durable bootstrap message. A registered agent with no
live process can require Herdr-side repair before a restart succeeds.

Before advancing a generation, recovery saves a plan for pending assignments
and individual replies already published by that worker. Those replies remain
acceptable through later restarts. Fresh publications from an old generation
remain rejected. Reply inspection reports the current worker generation for
stop and cleanup. Resolution freezes that generation in its disposition so
an exact retry still works after retirement.

Recovery delivers each unfinished assignment under a key containing its original
message ID and the target generation. The envelope retains its original body,
original message ID, and immediate predecessor ID. `task receive` adds a visible
redelivery marker and instructions to check prior effects before repeating work.
Workers reply against the new delivery ID. Caller-key retries return the existing
delivery, including handled deliveries, rather than reopening work. Unkeyed
assignments remain independent.

Every restart publishes a durable `task-restart` notice to the primary before
launch. The notice identifies the reservation, preserved replies, and replacement
assignments. An interrupted retry uses the saved plan and confirms replacements
before acknowledging their predecessors. Observation completes these obligations
before confirming a launch. An unfinished reply disposition blocks a new restart
until the primary retries its acceptance. Legacy launch reservations that already
advanced without a recovery plan cannot reconstruct their previous generation;
their notification omits that unavailable source.

Manual pane supervision uses the host session and the supplied launch arguments.
Task supervision uses the recorded session. Neither supervisor creates a pane.
A successful supervise command returns after the resident is ready. The host
keeps the resident running after the client exits. An identical start request
returns the active result. Stop cancels and joins the resident without closing
its worker. Stop forbids monitor flags. Explicit monitor flags on a start request
assert the corresponding host settings.

Manual supervision and raw Start reject task-bound panes. Control interrupt resolves an
agent alias to its pane before reserving the endpoint. An active resident blocks
raw control of its pane. Adoption reserves the task and endpoint. An identical
adoption retry can verify a matching active task worker without stopping its
resident. A manual resident blocks adoption. Restart arguments must include the
agent's resume identity when work needs to continue after a restart.

Lifecycle hooks can call `primary claim`, `primary verify`, and `primary release`
with hook JSON on stdin. These require SessionStart, Stop, and SessionEnd events,
respectively. The hook's working directory must equal the parent of the
Concierge state root. One live session owns the primary role. Repository skills
instruct secondary sessions to leave shared task supervision to that owner.
The default `concierge` command renders the root brief after state setup.

## Native checkpoints

A blocked initial worker remains an unadopted launch intent. Concierge reports
`checkpoint: folder_trust` for a recognized Claude workspace trust dialog and
`checkpoint: native_input` for other blocked input or unavailable screen text.
The response names the recorded session, pane, and worktree and reports whether
the primary notification was confirmed. It requires an operator decision in the
existing native dialog. After the dialog is resolved, retry the exact launch;
background reconciliation also continues the saved intent without creating a
second worker.

The host publishes a durable `task-native-checkpoint` message to its configured
primary, including checkpoints first observed during background reconciliation.
Retries retain one message per task generation and checkpoint kind. A generic
checkpoint can receive a separate folder-trust classification notice after a
screen read recovers. Native screen text is used only for bounded diagnostics;
it is never forwarded as instructions or used to approve a dialog. Concierge
keeps existing trust, permission, and sandbox settings intact. Task holds and
identity failures remain separate launch blockers.

## Automated worker launch

Queue the task first, then ask the host to lease a treehouse worktree and
launch its worker:

```sh
concierge launch task-1 --harness codex \
  --project /repo --branch task/task-1 \
  --base-commit FULL_COMMIT_ID --body-file brief.txt
```

By default, Concierge creates a task-owned workspace and runs the worker in its
first pane. The reserved workspace label supports recovery after a lost creation
response. Stop closes only the recorded worker pane. Herdr removes the workspace
when its last pane closes; any additional user panes keep the workspace open.
Cleanup then records the retired task. Existing shared workspaces remain
available through `--workspace WORKSPACE_ID`; labels are unsupported.

Treehouse is a required host dependency and is included in the Concierge Nix
package. The host selects the leased path. Fresh requests omit `--worktree`;
that option exists only for recovery of an already recorded raw-worktree launch.
The lease must be clean. Concierge creates the new task branch at the exact
base commit. Changed files, ignored-file conflicts, and existing branch conflicts
stop preparation without a forced reset.

Acquisition identity is recorded before requesting a lease. An uncertain request
recovers its existing lease by holder instead of acquiring another worktree.
Launch and restart verify the captured lease ID, holder, and path before native
startup. The task record retains the lease identity and result branch through restart
and retirement. A changed or missing lease requires inspection.

Use a full commit ID from the project and a new branch name. The host must have
an explicit Herdr session. `--harness claude` selects Claude. Repeat
`--agent-arg=VALUE` for optional launch arguments in their original order.
`--body-file -` reads the assignment from stdin. Claude arguments must not contain
`--settings`. Codex arguments must not override `shell_environment_policy`.
Configure other harness settings through the harness's user or project settings.

The host validates the request before reserving it. It captures the native
session ID from Herdr, adopts the worker, publishes its assignment, and registers
the task resident. Missing native session metadata leaves the launch unresolved.
Worker prompts contain the captured command paths, roots, recipient, and a check
of the inherited generation. They contain no assignment body.

The host continues tracked initial launches and retries the first resident
registration without a model request. A host restart reconstructs this work from
task records. A supervision stop removes the task from periodic recovery for the
current host lifetime. A later explicit retry can resume the same pending launch.
An active resident's failure does not create a fresh automatic restart budget.

Exact pending retries must preserve every request field. A completed or retired
launch requires inspection instead of another launch. A disconnected client
cannot establish whether launch completed. Inspect `backlog show TASK_ID` and
`worker show TASK_ID` before issuing another launch. The client never retries
automatically. Uncertain pane or agent creation is discovered without submitting
another creation attempt.

## Initial launch state

The task-state API preserves an initial launch request inside its authoritative
task record. Its phases are `reserved`, `preparing`, `lease_requested`,
`lease_ready`, `pane_requested`, and `agent_requested`. Each new reservation needs a fresh generation. Exact retries
retain that generation and request. Every progress transition checks the expected
generation. A rejected or uncertain write must stop the executor before further
external effects.

The shared adoption lock reserves worker names, worktrees, branches, creation
labels, and captured endpoints across tasks. Endpoint and native resume identity
are write-once. Only `reserved` intent permits cancellation without resource
reconciliation. A hold prevents new preparation or launch effects. Already-created
resources can still be recorded during a hold.

Intent remains alongside matching adoption and in-flight records. The caller
must confirm durable assignment publication before clearing it. Results and
ordinary restart are blocked during initial launch finalization. A potentially
submitted pane creation requires reconciliation; an empty lookup after a timeout
does not permit another creation attempt.


## Worktree lease cleanup

Accept the result, stop the worker, and retire the accepted generation:

```sh
concierge task stop task-1 --generation GEN
concierge backlog cleanup task-1 --generation GEN
```

Cleanup requires confirmation that the exact worker pane departed. For a closed
result, it returns the lease and records `lease: returned`. A retained result
keeps its lease and records `lease: held`. Unaccepted results keep their leases.
Removing a task hold does not release its worktree.

Explicitly release a retained result or an abandoned acquisition with:

```sh
concierge backlog release-worktree task-1 --generation GEN
```

The command uses the primary host admission and the recorded generation. It
preserves a newer worker and its supervision. The lease ID and holder must still
match. The pool must report no live processes. Return uses neither --force nor
approval to discard changes. The result branch remains in the repository.

Return requires a clean checkout, including ignored files. Hidden index flags,
detached result commits, changed branches, and dirty work prevent return and
keep the lease. Commit or move useful files before retrying. Treehouse 3.1.1 can
remove a task branch's ignore rules while parking its checkout, then delete
previously ignored files. Concierge checks those files before invoking return.

An abandoned acquisition whose response was lost retains its holder and original
base commit. Explicit release finds that holder's existing lease and captures its
identity before returning it. An unused detached checkout can return only when
its commit remains reachable from a branch, remote reference, or tag. Missing
or ambiguous acquisition identity requires pool inspection. Historical records
that lack the original base commit cannot infer it during release.

A successful pool return followed by an interrupted task write can complete on
an identical retry while the checkout remains unleased. A rebound lease rejects
that retry for inspection. Already recorded returns use no external tools.
