#!/usr/bin/env bash
# Thread/world-model edge-case gate.
#
# Runs the deterministic scenarios that protect the owner's complaint:
# a colleague replied on the owner's behalf, a CC'd reply, a delegate
# sending on behalf, auto-replies, trivial acknowledgements, an invisible
# shared mailbox, and the priority filter that must stop flagging the
# thread. No mailbox content is read and no model is called.
set -euo pipefail

cd "$(dirname "$0")/.."

tests='Test(Colleague|Delegate|Owner|Cc|Auto|Trivial|Visibility|Mailing|ThreadState|Threads)'

go test -count=1 -v \
  -run "$tests" \
  ./internal/threadintel \
  ./internal/store \
  ./internal/priority

go test -count=1 ./internal/agenda

# The world model must build on every platform, and the Outlook reader must
# keep compiling for Windows even when this runs on Linux or macOS.
go build ./...
GOOS=windows go build ./internal/source/outlook

echo "thread/world-model gate: green"
