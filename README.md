# Tincan

Tincan stores durable inbox messages and sends notifications to coding agents.
[Concierge](https://github.com/roman/concierge) uses Tincan for local task
supervision, worker assignments, and result handling.

| Command | Responsibility |
| --- | --- |
| `tincan inbox` | Send, list, wait for, read, and acknowledge messages. |
| `tincan notify` | Prompt a Herdr endpoint while its inbox has pending messages. |

## Install and verify

```sh
nix profile install .#tincan
tincan --version
tincan --help
```

The package exposes one public executable, `tincan`. Its runtime includes
pinned Herdr 0.9.3 for notifications. The Go module provides inbox storage,
notification behavior, the notification adapter, and CLI parsing. Concierge
owns task state and worker lifecycle operations in its separate repository.

```sh
nix build .#tincan
nix develop
nix fmt
go test ./...
nix flake check --system x86_64-linux
```

The development shell provides Go 1.25, golines, and Herdr. The formatter uses
100 columns. The flake gate verifies the full Go suite, source formatting,
transport package boundaries, and installed command behavior with an empty PATH.
Run `nix flake check --system aarch64-darwin` on a native Darwin builder too.

## Messages

Run from the project root or select an explicit message root:

```sh
export TINCAN_HOME="$PWD/.tincan"
tincan inbox send worker-1 --key request-1 --body 'Please check the task.'
tincan inbox list worker-1
tincan inbox read worker-1 MESSAGE_ID
tincan inbox ack worker-1 MESSAGE_ID
tincan inbox wait concierge --timeout 30s
```

`--root` overrides `TINCAN_HOME`. Without either setting, Tincan uses `.tincan`
in the current directory. Use `--body-file FILE` for a file body. An identical
keyed retry returns the existing message. Reusing a key with a different body
fails. Acknowledgement moves a handled message out of the pending inbox.

## Notifications

```sh
tincan notify --recipient worker-1 --target worker-1 --session work
```

`--session` selects the Herdr session. `HERDR_SESSION` supplies its default.
Notifications leave the durable message pending. The receiver reads and
acknowledges it through the inbox. The adapter supports session binding and
prompt submission. Concierge supplies worker launch, health, and recovery.
