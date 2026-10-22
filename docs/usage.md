# Command reference

| Command | Responsibility |
| --- | --- |
| `tincan inbox` | Send, list, wait for, read, and acknowledge messages. |
| `tincan notify` | Prompt a Herdr endpoint while its inbox has pending messages. |

## Messages

Select a message root with `--root` or `TINCAN_HOME`:

```sh
export TINCAN_HOME="$PWD/.tincan"
tincan inbox send worker-1 --key request-1 --body 'Please check the task.'
tincan inbox list worker-1
tincan inbox read worker-1 MESSAGE_ID
tincan inbox ack worker-1 MESSAGE_ID
tincan inbox wait concierge --timeout 30s
```

`--root` overrides `TINCAN_HOME`. Without either setting, Tincan uses `.tincan`
in the current directory. Use `--body-file FILE` for a file body, or
`--body-file -` for standard input. An identical keyed retry returns the existing
message. Reusing a key with a different body fails. Acknowledgement moves a
handled message out of the pending inbox.

`inbox list` returns all pending messages in creation order. Each row contains
the full message ID and a short `type`: the key prefix before the first colon,
limited to 32 Unicode characters plus an ellipsis. Keys without a nonempty
prefix use `message`. This label is a display hint supplied by the sender.
Applications must still validate message bodies and generations before acting.
Tincan does not interpret application routing keys or message bodies.

The list reports `total`, `returned`, and `truncated`. It currently returns all
pending messages, so `returned` equals `total` and `truncated` is `false`.
The detail hints preserve the selected root and recipient.

Use `tincan inbox list worker-1 --full` for the previous `count` and
`id,key,created_at` output, including complete keys and creation times.
`tincan inbox read worker-1 MESSAGE_ID` also returns the complete key and body.
Consumers of the previous CLI list schema must add `--full`. The Go inbox API
is unchanged.

## Notifications

```sh
tincan notify --recipient worker-1 --target worker-1 --session work
```

`--session` selects the Herdr session. `HERDR_SESSION` supplies its default.
Notifications leave the durable message pending. The receiver reads and
acknowledges the message through the inbox.

The Nix package provides the `tincan` executable and includes Herdr on its runtime
PATH. Tincan handles message transport. Applications such as Concierge own task
state and worker lifecycle operations.
