# Setup tab: harness registration (with a duplicate entry), shell hook, daemon service, adapters, agent access and the importers (first one selected).
Output ".vhs/setup.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri --tab setup" Enter
Sleep 3s
Down 19
Sleep 500ms
Show
Sleep 300ms
Screenshot "docs/screenshots/setup.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
