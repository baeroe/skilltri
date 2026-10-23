# Rules editor: the folder rules of a server, opened with r on the Servers tab.
Output ".vhs/rules.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri" Enter
Sleep 3s
Down 1
Sleep 300ms
Type "r"
Sleep 800ms
Show
Sleep 300ms
Screenshot "docs/screenshots/rules.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
