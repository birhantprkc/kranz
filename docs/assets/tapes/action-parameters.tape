# Regenerate from the repository root with a release-stamped binary in ./bin:
#   make build VERSION=v0.16.0 && vhs docs/assets/tapes/action-parameters.tape
# Pin VHS 0.11.0 until upstream 0.12.0's cancelled-render regression is fixed.
Output docs/assets/action-parameters.gif

Set Shell "bash"
Set FontSize 15
Set Width 1000
Set Height 620
Set Padding 16
Set LineHeight 1.2
Set TypingSpeed 25ms

Hide
Type "DEMO_ROOT=/tmp/kranz-action-parameters-demo"
Enter
Type `bin/kranz -C "$DEMO_ROOT" down >/dev/null 2>&1 || true`
Enter
Type `rm -rf "$DEMO_ROOT" && mkdir -p "$DEMO_ROOT"`
Enter
Type `cp examples/action-parameters/kranz.yaml examples/action-parameters/seed.sh "$DEMO_ROOT/"`
Enter
Type `printf '\nui:\n  theme: tokyo-night\n  accent: "#7DCFFF"\n  background: theme\n  color_mode: dark\n' >> "$DEMO_ROOT/kranz.yaml"`
Enter
Type `chmod +x "$DEMO_ROOT/seed.sh" && export PATH="$PWD/bin:$PATH" TERM=xterm-256color COLORTERM=truecolor FORCE_COLOR=1 && unset NO_COLOR`
Enter
Type `kranz -C "$DEMO_ROOT" up -d >/dev/null && cd "$DEMO_ROOT"; clear`
Enter
Sleep 1s
Show

Type "kranz"
Enter
Sleep 1500ms
Down
Enter
Down
Enter
Sleep 1200ms
Down
Down
Enter
Down
Down
Space
Sleep 900ms
Down
Down
Down
Enter
Sleep 1200ms
Up
Up
Up
Up
Up
Up
Up
Type "c"
Sleep 1200ms
Down
Enter
Backspace 3
Type "250"
Enter
Sleep 1200ms
Type "s"
Sleep 1200ms
Enter
Sleep 1800ms

Hide
Type "q"
Sleep 500ms
Type "d"
Sleep 500ms
Type `kranz -C "$DEMO_ROOT" down >/dev/null 2>&1 || true`
Enter
Type "cd /tmp"
Enter
Type `rm -rf "$DEMO_ROOT"`
Enter
Sleep 1s
