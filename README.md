# Tincan

[![CI](https://github.com/roman/tincan/actions/workflows/ci.yml/badge.svg)](https://github.com/roman/tincan/actions/workflows/ci.yml)

Tincan stores durable inbox messages and sends notifications to coding agents.
It provides a CLI and Go packages for inbox storage, notifications, and a shared
[Herdr client](docs/herdr.md).

## Install

Run directly with Nix:

```sh
nix run github:roman/tincan -- --help
nix profile install github:roman/tincan#tincan
```

Or add a flake input and use `inputs.tincan.packages.${system}.default`:

```nix
inputs.tincan.url = "github:roman/tincan";
```

## Usage

```sh
tincan --root .tincan inbox send worker-1 --key request-1 --body 'Please check the task.'
tincan --root .tincan inbox list worker-1
tincan --root .tincan inbox read worker-1 MESSAGE_ID
tincan --root .tincan inbox ack worker-1 MESSAGE_ID
```

For development, copy `.envrc.template` to `.envrc` and run `direnv allow`.
See the [command reference](docs/usage.md) and [development guide](docs/development.md).
