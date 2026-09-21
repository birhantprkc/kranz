# Action parameters

This example shows one action declaration that carries typed parameters and
projects them onto the command, the environment, and the TUI.

```sh
kranz -C examples/action-parameters actions list
kranz -C examples/action-parameters actions info infra/seed

kranz -C examples/action-parameters actions run infra/seed \
  --param env=prod \
  --param count=500 \
  --param write=true \
  --confirm
```

- `infra/seed` uses `select`, `number`, and `checkbox` controls in an `argv`
  template. The `{{count}}` element inserts that parameter's whole projection
  (`--count 500`), `--env={{env}}` inserts the value inside a literal, and
  `write` projects into the `WRITE` environment variable.
- `write` carries its own `confirm`, so the run asks only when it is switched
  on. `kranz actions info` lists such values under "Values that ask".
- `label` is a free-text parameter, which the TUI types in place.
- `checks/check` is a `radio` whose options each carry their own `run`, so the
  selected value chooses the command while `verbose` still projects a flag.
- The service `api` uses `infra/seed` as a `before_start` prerequisite with
  static parameter values that are validated when the configuration loads.

Parameter values are validated before confirmation and never become shell
source: a parameterized action is executed without a shell, so a value stays
one process argument.

To try the parameter tree, open the TUI here:

```sh
kranz -C examples/action-parameters
```

Open `infra`, then `seed` — the `◆` says its command is assembled from values,
and `c` opens them as a form with the command pinned above the fields. `Enter`
on `env` opens its values and `Space` marks one; `Space` on `write` toggles it;
`Enter` on `label` starts typing, where `Enter` keeps what was typed and `Esc`
restores the previous value. The `$` row is the exact invocation, and `s` runs
it.
