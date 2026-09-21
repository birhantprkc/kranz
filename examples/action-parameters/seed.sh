#!/bin/sh
# Prints the invocation it received, so the example shows exactly how each
# parameter reached the command.
echo "argv: $*"
echo "WRITE=${WRITE:-0}"
