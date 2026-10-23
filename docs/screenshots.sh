#!/usr/bin/env bash
# Regenerates the README screenshots in docs/screenshots/ from the VHS tapes in docs/tapes/.
#
#   make screenshots                    # all tapes
#   bash docs/screenshots.sh servers    # only docs/tapes/servers.tape
#
# Needs go, git, vhs (brew install vhs; pulls ttyd and ffmpeg) and optionally pngquant/oxipng.
#
# Everything runs in a throwaway sandbox HOME with skilltri's default dirs below it, so the real
# ~/.config/skilltri, ~/.claude and the keychain are never touched (the only secret reference sits on a
# disabled server and is never resolved). The demo data comes from docs/demo/seed.go: six MCP servers
# (fake stdio servers, one local Streamable HTTP server from docs/demo/httpmcp.go, one disabled, one
# plugin server that needs review), a local, a git and a marketplace skill source, folder rules and a
# Claude Code registration with a duplicate entry. A real daemon serves them and a real `skilltri mcp`
# session from ~/dev/acme-shop stays open while the tapes run.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

sandbox=$(mktemp -d /private/tmp/stdemo.XXXXXX) # resolved path, so the TUI can shorten it to ~
pids=()
alive() { # any recorded pid or any process started from the sandbox still running
	local p
	for p in ${pids[@]+"${pids[@]}"}; do kill -0 "$p" 2>/dev/null && return 0; done
	pgrep -f "$sandbox/" >/dev/null 2>&1
}
cleanup() {
	local status=$? p i
	trap - EXIT INT TERM HUP
	{ exec 3>&-; } 2>/dev/null || true
	if [ -x "$sandbox/home/.local/bin/skilltri" ]; then
		HOME="$sandbox/home" "$sandbox/home/.local/bin/skilltri" daemon stop >/dev/null 2>&1 || true
	fi
	# TERM everything (the recorded pids and the downstream servers the daemon started from the sandbox),
	# give it two seconds, then KILL what is left; wait only on our own, already dead children
	pkill -f "$sandbox/" 2>/dev/null || true
	for p in ${pids[@]+"${pids[@]}"}; do kill "$p" 2>/dev/null || true; done
	for i in 1 2 3 4 5 6 7 8 9 10; do
		alive || break
		sleep 0.2
	done
	if alive; then
		pkill -9 -f "$sandbox/" 2>/dev/null || true
		for p in ${pids[@]+"${pids[@]}"}; do kill -9 "$p" 2>/dev/null || true; done
	fi
	for p in ${pids[@]+"${pids[@]}"}; do wait "$p" 2>/dev/null || true; done
	rm -rf "$sandbox" "$repo/.vhs"
	exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

demo_home="$sandbox/home"
unset XDG_CONFIG_HOME XDG_STATE_HOME XDG_DATA_HOME XDG_CACHE_HOME XDG_RUNTIME_DIR
for v in $(env | sed -n 's/^\(SKILLTRI_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done
mkdir -p "$demo_home/.local/bin" "$sandbox/bin"
bin="$demo_home/.local/bin"

# build with the real HOME (Go caches); everything else only sees the sandbox HOME.
# The binaries go under the sandbox HOME, so the TUI shows ~/.local/bin/... paths.
go build -trimpath -o "$bin/skilltri" .
go build -trimpath -o "$sandbox/bin/fakemcp" ./internal/testutil/fakemcp/cmd/fakemcp
go build -trimpath -o "$sandbox/bin/httpmcp" docs/demo/httpmcp.go
go build -trimpath -o "$sandbox/bin/seed" docs/demo/seed.go

# one wrapper per demo stdio server, so the config holds plain commands like `docs-mcp`
# (stderr dropped: fakemcp logs a startup line there, which the TUI would show as the last error)
server() { # name tools [prompts] [resources]
	cat >"$bin/$1-mcp" <<EOF
#!/bin/sh
FAKEMCP_NAME=$1 FAKEMCP_TOOLS=$2 FAKEMCP_PROMPTS=${3:-} FAKEMCP_RESOURCES=${4:-} exec "$sandbox/bin/fakemcp" 2>/dev/null
EOF
	chmod +x "$bin/$1-mcp"
}
server docs search_docs,get_page,list_versions,get_changelog "" guides
server browser navigate,click,type_text,screenshot,read_console,close_tab
server notes search_notes,read_note,create_note,link_notes daily-summary
server wiki search_pages,get_page,recent_changes

real_home=$HOME
real_path=$PATH
export HOME="$demo_home" SKILLTRI_DEMO_HOME="$demo_home"
export PATH="$bin:/usr/bin:/bin:/usr/sbin:/sbin" # no real tool paths in the screenshots
export SKILLTRI_DEMO_PATH="$PATH"
cd "$HOME"

"$sandbox/bin/seed"

"$sandbox/bin/httpmcp" 127.0.0.1:47391 >"$sandbox/httpmcp.log" 2>&1 &
http_pid=$!
pids+=("$http_pid")
sleep 0.5
kill -0 "$http_pid" 2>/dev/null || { cat "$sandbox/httpmcp.log" >&2; echo "the demo HTTP server could not start (another screenshots run?)" >&2; exit 1; }

skilltri daemon >"$sandbox/daemon.out" 2>&1 &
pids+=($!)
for _ in $(seq 50); do
	skilltri status >/dev/null 2>&1 && break
	sleep 0.2
done
skilltri update

# a real MCP session from ~/dev/acme-shop that stays open while the tapes run
mkfifo "$sandbox/mcp.in"
(cd "$HOME/dev/acme-shop" && exec skilltri mcp <"$sandbox/mcp.in" >"$sandbox/mcp.out" 2>&1) &
mcp_pid=$!
pids+=("$mcp_pid")
# read-write, so opening never blocks even if the reader is already gone
exec 3<>"$sandbox/mcp.in"
sleep 0.3
kill -0 "$mcp_pid" 2>/dev/null || { cat "$sandbox/mcp.out" >&2; echo "skilltri mcp exited early" >&2; exit 1; }
send() { printf '%s\n' "$1" >&3; }
send '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2.1.308"}}}'
send '{"jsonrpc":"2.0","method":"notifications/initialized"}'
send '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
sleep 2
send '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"tickets_search_tickets","arguments":{"query":"checkout 502"}}}'
send '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"docs_search_docs","arguments":{"query":"payment webhooks"}}}'
sleep 1
skilltri status

tapes=("$@")
if [ ${#tapes[@]} -eq 0 ]; then
	for f in "$repo"/docs/tapes/*.tape; do
		[ "$(basename "$f")" = config.tape ] || tapes+=("$(basename "$f" .tape)")
	done
fi
cd "$repo"
mkdir -p docs/screenshots
# VHS sizes the terminal in pixels, and the font's cell width differs between machines and even between
# runs. So every tape runs from a copy in .vhs/ with the current pixel size, writes `tput cols`x`tput lines`
# to $SKILLTRI_DEMO_SIZE (see config.tape), and is run again with a corrected size until the terminal is
# exactly 120x34. config.tape holds the starting size.
export SKILLTRI_DEMO_SIZE="$sandbox/size"
cols=120 lines=34 pad=$(sed -n 's/^Set Padding //p' docs/tapes/config.tape)
width=$(sed -n 's/^Set Width //p' docs/tapes/config.tape)
height=$(sed -n 's/^Set Height //p' docs/tapes/config.tape)
mkdir -p .vhs
written=() # PNGs of this run; only these get optimized, so untouched screenshots keep their bytes
for t in "${tapes[@]}"; do
	for try in 1 2 3 4 5 6 7 8; do
		sed "s/^Set Width .*/Set Width $width/; s/^Set Height .*/Set Height $height/" docs/tapes/config.tape >.vhs/config.tape
		sed 's|^Source "docs/tapes/config.tape"|Source ".vhs/config.tape"|' "docs/tapes/$t.tape" >".vhs/$t.tape"
		shots=$(sed -n 's/^Screenshot "\(.*\)"$/\1/p' "docs/tapes/$t.tape")
		rm -f "$SKILLTRI_DEMO_SIZE" $shots
		# VHS keeps its browser in the real HOME; the tapes switch to the sandbox
		HOME="$real_home" PATH="$real_path" vhs ".vhs/$t.tape" >".vhs/$t.log" 2>&1 || { cat ".vhs/$t.log" >&2; exit 1; }
		size=$(cat "$SKILLTRI_DEMO_SIZE" 2>/dev/null || echo 0x0)
		c=${size%x*} l=${size#*x}
		echo "== $t: ${width}x${height} px -> ${c}x${l}"
		missing=0
		for shot in $shots; do
			[ -s "$shot" ] || { echo "$t: $shot was not written"; missing=1; }
		done
		if [ "$c" = "$cols" ] && [ "$l" = "$lines" ] && [ "$missing" = 0 ]; then
			for shot in $shots; do written+=("$repo/$shot"); done
			continue 2
		fi
		[ "$c" -gt 0 ] && [ "$l" -gt 0 ] || { echo "$t: no terminal size recorded" >&2; exit 1; }
		# cell size from this run, then aim for the middle of the target cell count
		# (only the dimension that is off, so a correct one does not drift)
		[ "$c" = "$cols" ] || width=$((pad * 2 + ((width - pad * 2) * (cols * 2 + 1) + c) / (c * 2)))
		[ "$l" = "$lines" ] || height=$((pad * 2 + ((height - pad * 2) * (lines * 2 + 1) + l) / (l * 2)))
	done
	echo "$t: giving up after 8 runs (terminal ${cols}x${lines} and all screenshots needed)" >&2
	exit 1
done
rm -rf .vhs
export PATH="$real_path"

if [ ${#written[@]} -gt 0 ]; then
	command -v pngquant >/dev/null && pngquant --force --skip-if-larger --quality 80-95 --ext .png "${written[@]}" || true
	command -v oxipng >/dev/null && oxipng -q -o 4 --strip safe "${written[@]}" || true
fi
ls -lh "$repo/docs/screenshots"
