package hook

import (
	"fmt"
	"sort"
	"strings"
)

// Render returns e as shell statements: for zsh and bash `export K='v'` and `unset K`, for fish
// `set -gx K 'v'` and `set -e K`, one per line, sets before unsets, each sorted by name. Values are
// single-quoted with the quotes (and, for fish, backslashes) escaped; Notes are never rendered.
// Any other shell gets the zsh/bash form.
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
	return b.String()
}

func posixQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func fishQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// Init returns the hook script for shell (zsh, bash or fish), to be evaluated in the shell's rc
// file. It exports SKILLTRI_HOOK=<shell> and re-runs `skilltri env` whenever the working directory
// changes (zsh: chpwd_functions plus one immediate call; bash: PROMPT_COMMAND with a $PWD check;
// fish: a function on the PWD variable plus one immediate call). skilltri's stderr is discarded.
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

const zshHook = `export SKILLTRI_HOOK=zsh
_skilltri_hook() {
  eval "$(command skilltri env --shell zsh 2>/dev/null)"
}
typeset -ag chpwd_functions
if (( ! ${chpwd_functions[(I)_skilltri_hook]} )); then
  chpwd_functions=(_skilltri_hook $chpwd_functions)
fi
_skilltri_hook
`

const bashHook = `export SKILLTRI_HOOK=bash
_skilltri_hook() {
  local previous_exit_status=$?
  if [[ "$PWD" != "${_SKILLTRI_PWD-}" ]]; then
    _SKILLTRI_PWD="$PWD"
    eval "$(command skilltri env --shell bash 2>/dev/null)"
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
    command skilltri env --shell fish 2>/dev/null | source
end
__skilltri_hook
`
