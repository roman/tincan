#!/usr/bin/env bash
set -euo pipefail

go list -buildvcs=false ./... > tincan-packages
while IFS= read -r package; do
  case "$package" in
    github.com/roman/tincan/cmd/tincan|github.com/roman/tincan/pkg/tincancli|github.com/roman/tincan/pkg/inbox|github.com/roman/tincan/pkg/notify|github.com/roman/tincan/pkg/herdr) ;;
    *)
      printf 'Tincan repository contains a non-transport package: %s\n' "$package" >&2
      exit 1
      ;;
  esac
done < tincan-packages

go list -deps ./cmd/tincan > tincan-dependencies
while IFS= read -r dependency; do
  case "$dependency" in
    github.com/roman/tincan/cmd/tincan|github.com/roman/tincan/pkg/tincancli|github.com/roman/tincan/pkg/inbox|github.com/roman/tincan/pkg/notify|github.com/roman/tincan/pkg/herdr) ;;
    github.com/roman/tincan/*|github.com/roman/concierge|github.com/roman/concierge/*)
      printf 'Forbidden Tincan dependency: %s\n' "$dependency" >&2
      exit 1
      ;;
  esac
done < tincan-dependencies
