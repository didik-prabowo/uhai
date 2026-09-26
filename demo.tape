# A recording of uhai, for the README. Run it with `make demo`, which needs
# VHS: `brew install vhs`.
#
# THREE THINGS TO KNOW BEFORE RECORDING
#
# 1. This records a real session against whatever provider you have
#    configured, so whatever the status row says about your model and your
#    gateway ends up in a public GIF. Check what `/model` shows first. Point
#    it at a provider you do not mind naming.
#
# 2. The output goes to local/, which is gitignored. A terminal GIF is
#    several megabytes and does not belong in the history of a repository
#    people clone. Drag the file into a GitHub comment, copy the URL it
#    hands back, and put that in the README — which is what the projects
#    with good demos all do.
#
# 3. The Sleep values are guesses at how long a model takes. Watch the
#    result once and tune them; a demo that cuts off mid-answer is worse
#    than no demo.

Output local/demo.gif

Set Shell zsh
Set FontSize 15
Set Width 1300
Set Height 760
Set Padding 24
Set Framerate 24
Set TypingSpeed 60ms

# Start from a clean prompt rather than whatever the shell had on screen.
Hide
Type "clear" Enter
Show

Type "uhai"
Enter
Sleep 3s

# First beat: a question that makes it read the project. What sells here is
# the tool calls appearing one by one, and the status row pricing the turn.
Type "what does cmd/uhai do, and why is it so short?"
Enter
Sleep 25s

# Second beat, and the one worth the recording: an edit stops and asks, and
# the question carries the diff. Nothing else in this program photographs as
# well, because it is the whole argument in one frame.
Type "add a comment above main() saying what this file is for"
Enter
Sleep 20s

# Let the permission question sit long enough to be read, then answer it.
Sleep 3s
Type "y"
Sleep 6s

Type "/exit"
Enter
Sleep 2s
