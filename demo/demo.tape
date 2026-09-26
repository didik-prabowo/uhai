# The recording in the README. `make demo` runs it, and needs VHS:
# `brew install vhs`.
#
# It talks to `demo/mock`, a scripted OpenAI-compatible endpoint, not to a
# real provider. Two reasons, and the second is the one that matters:
#
#   1. anyone can regenerate this GIF, with no key and no bill
#   2. recording against a real provider puts whatever `/model` says into a
#      public GIF — this repository spent a commit taking one private
#      gateway's name back out of its history, and a demo is the easiest way
#      to put another straight back in
#
# Only the model's words are scripted. uhai really runs the tool calls the
# mock asks for, so the file listing, the answer's subject, the diff in the
# permission question and the token counts are all real. The one edit it
# makes is reverted by `make demo` when the recording ends.

Output docs/assets/demo.gif

Set Shell zsh
Set FontSize 15
Set Width 1200
Set Height 700
Set Padding 20
Set Framerate 24
Set TypingSpeed 55ms

Hide
Type "export HOME=$PWD/local/demo-home PS1='$ '" Enter
Type "clear" Enter
Show

Type "./uhai"
Enter
Sleep 2500ms

# First: a question that makes it read the project. What sells is the tool
# calls appearing one at a time and the status row pricing the turn.
Type "what does cmd/uhai do, and why is it so short?"
Enter
Sleep 7s

# Second, and the reason for the recording: an edit stops and asks, and the
# question carries the diff. Nothing else here photographs as well, because
# it is the whole argument of the program in one frame.
Type "add a comment above main() saying what this file is for"
Enter
Sleep 4s

# Long enough to read the diff before answering it.
Sleep 4s
Type "y"
Sleep 4s

Type "/exit"
Enter
Sleep 1500ms
