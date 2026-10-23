# Preview tab: what a session in ~/dev/acme-shop gets, and which rule decided.
Output ".vhs/preview.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri --tab preview" Enter
Sleep 3s
Down 1
Sleep 500ms
Show
Sleep 300ms
Screenshot "docs/screenshots/preview.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
