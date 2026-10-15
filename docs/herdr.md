# Shared Herdr client

`github.com/roman/tincan/pkg/herdr` owns the reusable command transport.
Applications add typed operations around this client. Tincan does not implement
worker launch, agent inspection, tabs, workspaces, health checks, or screen reads.

- `NewClient(executable)` selects a binary. An empty name selects `herdr`.
- `Runner` supports injected command execution with stdout, stderr, and an error.
- `SessionRunner` supports explicit session execution.
- `Client.ForSession(session)` returns a bound copy. The command runner removes
  inherited Herdr socket, workspace, tab, and pane selectors before execution.
- `Client.Run(ctx, args...)` executes arbitrary subcommands. It returns stdout
  unchanged on success and maps command failures to sentinel errors.
- `ResponseEnvelope[T]` and `ResponseError` describe the common JSON wrapper.
- `DecodeResponse[T](data)` decodes a required, non-null result. It reports
  malformed results as `ErrResponse` and maps error envelopes to sentinel errors.
- `Client.Prompt(ctx, target, message)` submits a notification.

An application can embed `herdr.Client` in its own client and add domain methods:

```go
type Client struct {
    herdr.Client
}

func (client Client) Pane(ctx context.Context, id string) (Pane, error) {
    data, err := client.Run(ctx, "pane", "get", id)
    if err != nil {
        return Pane{}, err
    }
    result, err := herdr.DecodeResponse[struct {
        Pane Pane `json:"pane"`
    }](data)
    return result.Pane, err
}
```

The application defines `Pane` and validates the fields its operation requires.
Rebind the embedded client with `ForSession` when creating a session-specific
application client.

Use `errors.Is` with `ErrCommand`, `ErrNotFound`, `ErrWorkspaceNotFound`,
`ErrAgentNotReady`, and `ErrResponse`. Agent-not-found errors retain the existing
`ErrNotFound` classification. Workspace and readiness errors also match
`ErrCommand`. Unknown Herdr errors match `ErrCommand`.
