# Shared settings, included by the other tapes (Source). Run the tapes through docs/screenshots.sh.
# Width and Height are the starting size: screenshots.sh calibrates them to a 120x34 cell terminal.
Set Shell bash
Set FontFamily "Menlo"
Set FontSize 15
Set Width 1271
Set Height 665
Set Padding 20
Set Theme "Dracula"
Set TypingSpeed 20ms
Set CursorBlink false

# switch to the sandbox HOME and PATH prepared by docs/screenshots.sh; the terminal size goes to a file
# the script checks (VHS sometimes measures the font too wide and the terminal gets fewer columns)
Hide
Type 'export HOME="$SKILLTRI_DEMO_HOME" PATH="$SKILLTRI_DEMO_PATH" && echo "$(tput cols)x$(tput lines)" >"$SKILLTRI_DEMO_SIZE" && cd ~/dev/acme-shop && clear' Enter
Sleep 300ms
Show
