package hook

import (
	"fmt"
	"sort"
	"strings"
)

// Render returns e as shell statements: for zsh and bash `export K='v'` and `unset K`, for fish
// `set -gx K 'v'` and `set -e K`, one per line, sets before unsets, each sorted by name; then the
// Local variables sorted by name, as unexported shell variables (`K='v'`, fish `set -g K 'v'`) or,
// when empty, unset. Values are single-quoted with the quotes (and, for fish, backslashes)
// escaped; Notes are never rendered. Any other shell gets the zsh/bash form.
func Render(e Env, shell string) string {
	keys := make([]string, 0, len(e.Set))
	for k := range e.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	unset := append([]string(nil), e.Unset...)
	sort.Strings(unset)

	var b strings.Builder
	for _, k := range keys {
		if shell == "fish" {
			fmt.Fprintf(&b, "set -gx %s %s\n", k, fishQuote(e.Set[k]))
		} else {
			fmt.Fprintf(&b, "export %s=%s\n", k, posixQuote(e.Set[k]))
		}
	}
	for _, k := range unset {
		if shell == "fish" {
			fmt.Fprintf(&b, "set -e %s\n", k)
		} else {
			fmt.Fprintf(&b, "unset %s\n", k)
		}
	}
	local := make([]string, 0, len(e.Local))
	for k := range e.Local {
		local = append(local, k)
	}
	sort.Strings(local)
	for _, k := range local {
		switch v := e.Local[k]; {
		case v == "" && shell == "fish":
			fmt.Fprintf(&b, "set -e %s\n", k)
		case v == "":
			fmt.Fprintf(&b, "unset %s\n", k)
		case shell == "fish":
			fmt.Fprintf(&b, "set -g %s %s\n", k, fishQuote(v))
		default:
			fmt.Fprintf(&b, "%s=%s\n", k, posixQuote(v))
		}
	}
	return b.String()
}

func posixQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// Init returns the hook script for shell (zsh, bash or fish), to be evaluated in the shell's rc
// file. It exports SKILLTRI_HOOK=<shell> and re-runs `skilltri env` whenever the working directory
// changes (zsh: chpwd_functions plus one immediate call; bash: PROMPT_COMMAND with a $PWD check;
// fish: a function on the PWD variable plus one immediate call), and before a prompt when
// config.json or catalog.json is newer than this shell's stamp or the Claude view in use is gone
// (zsh: precmd_functions, bash: the same PROMPT_COMMAND, fish: the fish_prompt event). The prompt
// check uses shell builtins only (`-nt` and `-d`; fish: `path mtime`, fish 3.5 or newer), so
// nothing is started when nothing changed. Each run passes the shell's PID in StampPIDVar, for
// Stamp. skilltri's stderr is discarded.
func Init(shell string) (string, error) {
	switch shell {
	case "zsh":
		return zshHook, nil
	case "bash":
		return bashHook, nil
	case "fish":
		return fishHook, nil
	}
	return "", fmt.Errorf("unsupported shell %q (supported: zsh, bash, fish)", shell)
}

// StampPIDVar carries the hook shell's PID to `skilltri env`, which then calls Stamp.
const StampPIDVar = "SKILLTRI_HOOK_PID"

const zshHook = `export SKILLTRI_HOOK=zsh
_skilltri_hook() {
  eval "$(SKILLTRI_HOOK_PID=$$ command skilltri env --shell zsh 2>/dev/null)"
}
_skilltri_precmd() {
  [[ -n "${_SKILLTRI_STAMP-}" ]] || return 0
  if [[ ! -e "$_SKILLTRI_STAMP" || "${_SKILLTRI_CONFIG-}" -nt "$_SKILLTRI_STAMP" || "${_SKILLTRI_CATALOG-}" -nt "$_SKILLTRI_STAMP" ]] ||
    [[ -n "${_SKILLTRI_VIEW-}" && ! -d "$_SKILLTRI_VIEW" ]]; then
    _skilltri_hook
  fi
}
typeset -ag chpwd_functions precmd_functions
if (( ! ${chpwd_functions[(I)_skilltri_hook]} )); then
  chpwd_functions=(_skilltri_hook $chpwd_functions)
fi
if (( ! ${precmd_functions[(I)_skilltri_precmd]} )); then
  precmd_functions=(_skilltri_precmd $precmd_functions)
fi
_skilltri_hook
`

const bashHook = `export SKILLTRI_HOOK=bash
_skilltri_stale() {
  [[ -n "${_SKILLTRI_STAMP-}" ]] || return 1
  if [[ ! -e "$_SKILLTRI_STAMP" || "${_SKILLTRI_CONFIG-}" -nt "$_SKILLTRI_STAMP" || "${_SKILLTRI_CATALOG-}" -nt "$_SKILLTRI_STAMP" ]]; then
    return 0
  fi
  [[ -n "${_SKILLTRI_VIEW-}" && ! -d "$_SKILLTRI_VIEW" ]]
}
_skilltri_hook() {
  local previous_exit_status=$?
  if [[ "$PWD" != "${_SKILLTRI_PWD-}" ]] || _skilltri_stale; then
    _SKILLTRI_PWD="$PWD"
    eval "$(SKILLTRI_HOOK_PID=$$ command skilltri env --shell bash 2>/dev/null)"
  fi
  return $previous_exit_status
}
if [[ ";${PROMPT_COMMAND[*]:-};" != *";_skilltri_hook;"* ]]; then
  if [[ "$(declare -p PROMPT_COMMAND 2>&1)" == "declare -a"* ]]; then
    PROMPT_COMMAND=(_skilltri_hook "${PROMPT_COMMAND[@]}")
  else
    PROMPT_COMMAND="_skilltri_hook${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
  fi
fi
`

const fishHook = `set -gx SKILLTRI_HOOK fish
function __skilltri_hook --on-variable PWD
    SKILLTRI_HOOK_PID=$fish_pid command skilltri env --shell fish 2>/dev/null | source
end
function __skilltri_prompt --on-event fish_prompt
    set -q _SKILLTRI_STAMP; or return
    set -l stamp (path mtime -- $_SKILLTRI_STAMP 2>/dev/null)
    if test -z "$stamp"
        __skilltri_hook
        return
    end
    for f in $_SKILLTRI_CONFIG $_SKILLTRI_CATALOG
        set -l m (path mtime -- $f 2>/dev/null)
        if test -n "$m"; and test "$m" -gt "$stamp"
            __skilltri_hook
            return
        end
    end
    if set -q _SKILLTRI_VIEW; and not test -d "$_SKILLTRI_VIEW"
        __skilltri_hook
    end
end
__skilltri_hook
`
