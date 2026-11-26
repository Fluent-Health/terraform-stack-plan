#!/bin/sh
# Runs the Rust ui binary named by $UI_BIN inside the toolchain image it was built in (its
# glibc can be newer than the host's), on the host network, with the replay's environment.
# Usage: UI_BIN=/path/to/ui in-docker.sh -config <file> -addr <addr>
set -eu
exec docker run --rm --init --network host -u "$(id -u):$(id -g)" \
  -e HOME -e CLOUDSDK_CONFIG \
  -e TFSTACKPLAN_UI_SESSION_SECRET -e TFSTACKPLAN_UI_OAUTH_SECRET \
  -e TFSTACKPLAN_UI_GITHUB_WEBHOOK_SECRET \
  -v /tmp:/tmp -v "$UI_BIN:/ui:ro" rust:1.98.1-slim-trixie /ui "$@"
