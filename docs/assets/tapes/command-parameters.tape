# Regenerate from the repository root with a release-stamped binary in ./bin:
#   make build VERSION=v0.16.0 && vhs docs/assets/tapes/command-parameters.tape
# Pin VHS 0.11.0 until upstream 0.12.0's cancelled-render regression is fixed.
Output docs/assets/command-parameters.gif

Set Shell "bash"
Set FontSize 15
Set Width 1000
Set Height 620
Set Padding 16
Set LineHeight 1.2
Set TypingSpeed 18ms

Hide
Type "DEMO_ROOT=/tmp/kranz-command-parameters-demo"
Enter
Type `rm -rf "$DEMO_ROOT" && mkdir -p "$DEMO_ROOT/project" "$DEMO_ROOT/tmp"`
Enter
Type `cp examples/action-parameters/kranz.yaml examples/action-parameters/seed.sh "$DEMO_ROOT/project/" && chmod +x "$DEMO_ROOT/project/seed.sh"`
Enter
Type `export TMPDIR="$DEMO_ROOT/tmp" PATH="$PWD/bin:$PATH" TERM=xterm-256color COLORTERM=truecolor FORCE_COLOR=1 && unset NO_COLOR`
Enter
Type `kranz -C "$DEMO_ROOT/project" up -d >/dev/null`
Enter
Type "clear"
Enter
Sleep 1s
Show

Type "kranz -C /tmp/kranz-command-parameters-demo/project actions info infra/seed"
Enter
Sleep 2500ms
Type "kranz -C /tmp/kranz-command-parameters-demo/project actions run infra/seed --param env=staging --param count=250 --param label=release-check"
Enter
Sleep 2500ms

Hide
Type `kranz -C "$DEMO_ROOT/project" down >/dev/null 2>&1 || true`
Enter
Type `rm -rf "$DEMO_ROOT"`
Enter
Sleep 1s
