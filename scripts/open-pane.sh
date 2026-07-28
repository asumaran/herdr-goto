#!/bin/sh
# Entry point for the "open" plugin action: opens the goto pane (placement and
# size come from the [[panes]] entry in herdr-plugin.toml).
# Plugin commands are argv arrays with no shell expansion, so this wrapper
# exists to resolve HERDR_BIN_PATH at runtime.
set -eu
exec "${HERDR_BIN_PATH:-herdr}" plugin pane open --plugin asumaran.goto --entrypoint goto
