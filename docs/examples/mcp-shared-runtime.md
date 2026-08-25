# MCP shared-runtime example

This local-only example proves that the CLI and an MCP client operate the same
Kranz runtime. It starts three harmless Python/shell services on localhost,
runs one action, reads its numbered result again, and cleans up the session.

<div class="demo-frame">

![A user asks a coding agent about a live Kranz session; the agent checks API readiness, runs a migration, and safely re-reads its result through MCP](../assets/mcp-shared-runtime.gif)

</div>

## Run it

From the repository root, build Kranz first:

```bash
make build VERSION=v0.9.0
cd examples/mcp-shared-runtime
```

Then use separate terminals if you want to keep the TUI visible:

```bash
../../bin/kranz up -d
../../bin/kranz attach                                      # terminal 1: TUI
../../bin/kranz status                                      # terminal 2: CLI
KRANZ_BIN=../../bin/kranz python3 mcp_client.py status      # terminal 3: MCP
KRANZ_BIN=../../bin/kranz python3 mcp_client.py migrate
../../bin/kranz down
```

The views report one session identity. The action result and logs use the same
`OWNER/ACTION#run`; the client's second result read does not execute the action
again. Closing an MCP client attached to the TUI-owned runtime leaves the TUI
and services running.

Point the same minimal client at another project with absolute paths:

```bash
KRANZ_BIN=/path/to/kranz \
KRANZ_PROJECT_DIR=/path/to/project \
python3 mcp_client.py actions
```

## Ports and cleanup

The example listens only on `127.0.0.1:18931` and `127.0.0.1:18932`. Run
`../../bin/kranz down` from the example directory when finished. If startup
reports a port conflict, inspect the owner instead of changing the example's
ports silently:

```bash
../../bin/kranz port inspect 18931
../../bin/kranz port inspect 18932
```

## Regenerate the recording

The recording is built from the same live client and project, with no fixture
JSON or personal environment data:

```bash
make build VERSION=v0.9.0
vhs docs/assets/tapes/mcp-shared-runtime.tape
```

The smoke evidence is recorded in
[`docs/assets/demo/mcp-smoke.md`](https://github.com/kranz-org/kranz/blob/main/docs/assets/demo/mcp-smoke.md),
and the runnable source lives in
[`examples/mcp-shared-runtime`](https://github.com/kranz-org/kranz/tree/main/examples/mcp-shared-runtime).
