# Doctor tab: the results of skilltri doctor.
Output ".vhs/doctor.gif"
Source "docs/tapes/config.tape"

Hide
Type "skilltri --tab doctor" Enter
Sleep 4s
Show
Sleep 300ms
Screenshot "docs/screenshots/doctor.png"
Sleep 300ms # VHS takes the screenshot on a later frame; do not end the tape before it
