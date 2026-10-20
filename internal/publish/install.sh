#!/bin/sh
# Installs the Telesphorepo CLI of the server @@URL@@ on Linux and macOS.
#
#   curl -fsSL @@URL@@/install.sh | sh
#   curl -fsSL @@URL@@/install.sh | sh -s -- setup 'LINK'
#
# The script downloads the archive of this OS and CPU from the server, checks its SHA-256, and
# installs telesphorepo into ~/.local/bin with docker-credential-telesphorepo next to it. When
# that folder is not on PATH, it offers to add it in the start-up file of your shell. In GitHub
# Actions it adds the folder to the PATH of the later steps. Arguments after "--" go to the CLI,
# which runs once the install is done.
set -eu

URL='@@URL@@'

# sum_of prints the SHA-256 of an archive of this server's build.
sum_of() {
	case "$1" in
@@SUMS@@
	*) echo "" ;;
	esac
}

fail() {
	echo "telesphorepo: $*" >&2
	exit 1
}

has_tty() {
	(: </dev/tty) 2>/dev/null
}

# add_to_path puts ~/.local/bin on the PATH of the user's shell ($SHELL): it appends one line to
# the start-up file that the shell reads, after a question on the terminal, and only once. With
# no terminal it prints the line and the file.
add_to_path() {
	line='export PATH="$HOME/.local/bin:$PATH"'
	shell=${SHELL:-sh}
	case "${shell##*/}" in
	zsh)
		# Terminal on macOS opens login shells (~/.zprofile); terminals on Linux open interactive
		# shells that are no login shells (~/.zshrc).
		if [ "$1" = darwin ]; then rc=.zprofile; else rc=.zshrc; fi
		;;
	bash)
		if [ "$1" = darwin ]; then rc=.bash_profile; else rc=.bashrc; fi
		;;
	fish)
		rc=.config/fish/config.fish
		line='set -gx PATH $HOME/.local/bin $PATH'
		;;
	*) rc=.profile ;;
	esac
	file="$HOME/$rc"

	if grep -qsxF "$line" "$file"; then
		echo "telesphorepo: ~/$rc puts ~/.local/bin on PATH; open a new terminal, or run now:" >&2
		echo "  $line" >&2
		return 0
	fi
	answer=n
	if has_tty; then
		printf 'Add ~/.local/bin to your PATH in ~/%s? [Y/n] ' "$rc" >/dev/tty
		read -r answer </dev/tty || answer=n
		case "$answer" in
		'' | [Yy] | [Yy][Ee][Ss]) answer=y ;;
		esac
	fi
	if [ "$answer" = y ]; then
		mkdir -p "$(dirname "$file")"
		if [ -s "$file" ] && [ -n "$(tail -c 1 "$file")" ]; then
			echo >>"$file"
		fi
		echo "$line" >>"$file"
		echo "telesphorepo: added ~/.local/bin to PATH in ~/$rc; open a new terminal, or run now:" >&2
		echo "  $line" >&2
		return 0
	fi
	echo "telesphorepo: ~/.local/bin is not on PATH. Add this line to ~/$rc, then open a new terminal:" >&2
	echo "  $line" >&2
}

main() {
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "this script installs on Linux and macOS; on Windows, run: irm $URL/install.ps1 | iex" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "the server has no CLI for the CPU $(uname -m)" ;;
	esac
	name="telesphorepo_${os}_${arch}.tar.gz"
	want=$(sum_of "$name")
	[ -n "$want" ] || fail "the server has no CLI for $os/$arch"

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$URL/cli/$name" -o "$tmp/$name" || fail "the download of $URL/cli/$name failed"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$tmp/$name" "$URL/cli/$name" || fail "the download of $URL/cli/$name failed"
	else
		fail "install curl or wget, then run this script again"
	fi

	if command -v sha256sum >/dev/null 2>&1; then
		got=$(sha256sum "$tmp/$name" | cut -d ' ' -f 1)
	elif command -v shasum >/dev/null 2>&1; then
		got=$(shasum -a 256 "$tmp/$name" | cut -d ' ' -f 1)
	elif command -v openssl >/dev/null 2>&1; then
		got=$(openssl dgst -sha256 -r "$tmp/$name" | cut -d ' ' -f 1)
	else
		fail "install sha256sum, shasum or openssl to check the download, then run this script again"
	fi
	[ "$got" = "$want" ] || fail "the SHA-256 of $name is $got, but the server names $want; nothing was installed"

	tar -xzf "$tmp/$name" -C "$tmp" telesphorepo || fail "$name does not hold telesphorepo"
	dir="$HOME/.local/bin"
	mkdir -p "$dir"
	chmod 755 "$tmp/telesphorepo"
	mv -f "$tmp/telesphorepo" "$dir/telesphorepo.new"
	mv -f "$dir/telesphorepo.new" "$dir/telesphorepo"
	helper="$dir/docker-credential-telesphorepo"
	rm -f "$helper"
	ln -s telesphorepo "$helper" 2>/dev/null || cp "$dir/telesphorepo" "$helper"
	rm -rf "$tmp"
	trap - EXIT
	echo "telesphorepo: installed the CLI of $URL in $dir" >&2

	if [ -n "${GITHUB_PATH:-}" ]; then
		echo "$dir" >>"$GITHUB_PATH"
	else
		case ":$PATH:" in
		*":$dir:"*) ;;
		*) add_to_path "$os" ;;
		esac
	fi

	[ "$#" -gt 0 ] || return 0
	# The script itself came on stdin, so the CLI reads the terminal when there is one.
	if has_tty; then
		exec "$dir/telesphorepo" "$@" </dev/tty
	fi
	exec "$dir/telesphorepo" "$@"
}

main "$@"
