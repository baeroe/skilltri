# Servers tab: the gateway's MCP servers with status and rules, details of the selected one.
Output ".vhs/servers.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri" Enter
Sleep 3s
Down 4
Sleep 500ms
Show
Sleep 300ms
Screenshot "docs/screenshots/servers.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
