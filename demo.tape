# demo.tape — generates the README demo GIF (assets/demo.gif).
#
# Run:
#   brew install vhs            # one-time (https://github.com/charmbracelet/vhs)
#   go build -o smolbill ./cmd/smolbill
#   vhs demo.tape              # writes assets/demo.gif
#
# The point of the GIF: in one command, smolbill computes a real invoice and then
# PROVES the meter and the invoice agree, with a hash. That proof is the whole pitch,
# so the demo leads with it.

Output assets/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 820
Set Padding 24
Set Theme "Catppuccin Mocha"
Set TypingSpeed 55ms

Type "# smolbill: meter usage, send a correct bill, and prove it never drifted"
Enter
Sleep 1500ms

Type "./smolbill demo"
Enter
Sleep 5s

# Hold on the final frame — the reconciliation hash + the MATCH proof line.
Sleep 3s
