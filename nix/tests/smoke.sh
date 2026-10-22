#!/usr/bin/env bash
set -euo pipefail

unset TINCAN_HOME HERDR_SESSION HERDR_SOCKET_PATH HERDR_WORKSPACE_ID HERDR_TAB_ID HERDR_PANE_ID
export PATH=
test -x "$tincan/bin/tincan"
for flag in --version -v -V; do
  test "$("$tincan/bin/tincan" "$flag")" = "$version"
done
"$tincan/bin/tincan" --root "$TMPDIR/messages" inbox send smoke --body 'Packaged message' > sent
"$grep" -q '^id: ' sent
"$tincan/bin/tincan" --root "$TMPDIR/messages" inbox list smoke > pending
"$grep" -q '^total: 1$' pending
"$tincan/bin/tincan" --root "$TMPDIR/messages" inbox list smoke --full > full
"$grep" -q '^count: 1$' full
"$tincan/bin/tincan" notify --help >/dev/null
