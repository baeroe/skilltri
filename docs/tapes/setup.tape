# Setup tab: harness registration (with a duplicate entry), the shell hook (selected), daemon service, adapters.
Output ".vhs/setup.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri --tab setup" Enter
Sleep 3s
Down 7
Sleep 500ms
Show
Sleep 300ms
Screenshot "docs/screenshots/setup.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
