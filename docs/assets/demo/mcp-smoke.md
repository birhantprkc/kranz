# MCP demo smoke evidence

Recorded: 2026-08-24 on macOS arm64 with the working-tree `./bin/kranz`.

The deterministic client in `examples/mcp-shared-runtime/mcp_client.py` started
`kranz mcp`, negotiated MCP `2025-11-25`, and used only resource/tool calls.
Against one live background runtime it observed:

```text
mode=attached session=1b833936
api running pid=92375
api stderr: database connection refused; retrying
restart targets=api,web,worker
api ready pid=92403
api/migrate#1 succeeded exit=0
migration complete; schema=42
read again: same run, no re-execution
api/migrate#1 succeeded
```

The session was then stopped through the ordinary terminal CLI. The tapes use
the same client and example, so regenerated frames cannot substitute fixture
JSON for a Kranz result. No vendor-specific coding client is named in public
copy until its real configuration has an additional smoke record.
