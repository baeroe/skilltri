# Skills tab: sources, plugins and skills as a tree, details of the selected node.
Output ".vhs/skills.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri --tab skills" Enter
Sleep 3s
Down 6
Sleep 500ms
Show
Sleep 300ms
Screenshot "docs/screenshots/skills.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
