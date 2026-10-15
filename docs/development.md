# Development

Copy `.envrc.template` to `.envrc` and run `direnv allow`, or enter the shell with
`nix develop`. The shell provides Go, gopls, golines, actionlint, and Herdr.

```sh
nix develop -c go test ./...
nix fmt
nix develop -c actionlint
nix flake check
```

The formatter uses 100 columns. The flake checks run the Go suite, verify source
formatting and transport package boundaries, and exercise the installed command
with an empty PATH. CI runs `nix flake check` on Linux and macOS. All four default
Nix systems expose `packages.tincan` and `packages.default`.

## Nix layout

`flake.nix` wires flake-parts and nixDir. Package definitions live in
`nix/packages/`, the shell in `nix/devshells/`, and checks and the formatter in
`nix/checks.nix`. Shell checks live in `nix/tests/`.

The `tincan-command` package builds and tests the Go command. The `tincan` package
wraps it with Herdr on PATH. The package boundary check permits only the CLI,
inbox, notifications, and shared Herdr client. It rejects Concierge dependencies.

## Version

`VERSION` is the release version source. Nix reads it for package names and
passes it to `pkg/tincancli.version` with Go linker flags. Direct `go run` and
unversioned Go builds report `dev`. The installed-command check compares all
three version flags against `VERSION`.

For a versioned build outside Nix:

```sh
go build -ldflags "-X github.com/roman/tincan/pkg/tincancli.version=$(cat VERSION)" ./cmd/tincan
```
