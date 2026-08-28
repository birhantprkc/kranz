# MCP shared-runtime example

This local-only project proves that Kranz CLI and MCP calls reach one runtime.
It uses Python 3, `/bin/sh`, and localhost ports `18931` and `18932`; it needs no
credentials or network access.

From the repository root:

```bash
make build VERSION=v0.11.0
cd examples/mcp-shared-runtime
../../bin/kranz up -d
../../bin/kranz status
KRANZ_BIN=../../bin/kranz python3 mcp_client.py status
KRANZ_BIN=../../bin/kranz python3 mcp_client.py migrate
../../bin/kranz down
```

The Python client starts an unbound MCP server, negotiates over stdio, and calls
real resources and tools. Set `KRANZ_RUNTIME` to a name returned by `runtimes`
to address another live project without restarting the server.
It is deliberately small enough to audit; it is demo code, not a general MCP
SDK. See the [published walkthrough](https://kranz-org.github.io/kranz/examples/mcp-shared-runtime)
for the three-terminal TUI/CLI/MCP flow and cleanup notes.
