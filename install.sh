#!/bin/sh
# Installs memory: the command line, and its skill and hooks for every agent
# found on this machine (Claude Code, Codex, Antigravity).
#
#   curl -fsSL https://raw.githubusercontent.com/river-ranx/Personal-Memory-Plugin/main/install.sh | sh
#
# Settings, all optional:
#   MEMORY_VERSION   a release tag such as v0.1.0 (default: the latest release)
#   MEMORY_BIN_DIR   where the command goes (default: ~/.local/bin)
#   MEMORY_AGENTS    which agents to set up, e.g. "claude codex" (default: those found)
#
# Run it again to upgrade. The memories themselves (~/.memory) are never touched.

set -eu

repo="river-ranx/Personal-Memory-Plugin"

say() { printf '%s\n' "$*"; }
fail() { printf 'memory install: %s\n' "$*" >&2; exit 1; }

# The whole script is one function, called on the last line: a download cut
# short runs nothing rather than half of it.
main() {
	command -v curl >/dev/null 2>&1 || fail "curl is needed"

	case "$(uname -s)" in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) fail "unsupported system $(uname -s): macOS and Linux are" ;;
	esac
	case "$(uname -m)" in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*) fail "unsupported processor $(uname -m): arm64 and amd64 are" ;;
	esac
	asset="memory-$os-$arch"

	version="${MEMORY_VERSION:-latest}"
	if [ "$version" = latest ]; then
		base="https://github.com/$repo/releases/latest/download"
	else
		base="https://github.com/$repo/releases/download/$version"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	say "Downloading $asset ($version)…"
	curl -fsSL "$base/$asset" -o "$tmp/memory" || fail "could not download $base/$asset"
	curl -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || fail "could not download the checksums"
	verify "$tmp" "$asset"

	bin_dir="${MEMORY_BIN_DIR:-$HOME/.local/bin}"
	mkdir -p "$bin_dir"
	chmod 755 "$tmp/memory"
	# Replace, not overwrite: a running memory keeps its old file.
	mv -f "$tmp/memory" "$bin_dir/memory"
	bin="$bin_dir/memory"
	say "Installed $bin"

	agents="${MEMORY_AGENTS:-$(found_agents)}"
	if [ -z "$agents" ]; then
		say "No Claude Code, Codex or Antigravity found. Set one up later with: $bin install <claude|codex|antigravity>"
	else
		# shellcheck disable=SC2086 # one word per agent
		"$bin" install $agents
	fi

	case ":$PATH:" in
	*":$bin_dir:"*) ;;
	*) say "Note: $bin_dir is not on your PATH. The agents call $bin by its full path; to type \`memory\` yourself, add it to PATH." ;;
	esac
	say "Done. Memories are kept in ~/.memory/memory.db, created on first use."
}

# verify checks the download against the release's SHA256SUMS.
verify() {
	want=$(awk -v f="$2" '$2 == f { print $1 }' "$1/SHA256SUMS")
	[ -n "$want" ] || fail "no checksum for $2 in the release"
	if command -v shasum >/dev/null 2>&1; then
		got=$(shasum -a 256 "$1/memory" | awk '{ print $1 }')
	elif command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$1/memory" | awk '{ print $1 }')
	else
		fail "shasum or sha256sum is needed to check the download"
	fi
	[ "$got" = "$want" ] || fail "checksum mismatch for $2: the download is not the released file"
}

# found_agents is each agent with a home or a command on this machine.
found_agents() {
	list=""
	if [ -d "$HOME/.claude" ] || command -v claude >/dev/null 2>&1; then list="$list claude"; fi
	if [ -d "${CODEX_HOME:-$HOME/.codex}" ] || command -v codex >/dev/null 2>&1; then list="$list codex"; fi
	if [ -d "$HOME/.gemini" ] || command -v agy >/dev/null 2>&1; then list="$list antigravity"; fi
	printf '%s' "${list# }"
}

main "$@"
