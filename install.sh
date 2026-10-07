#!/bin/sh
# OmniBox AI: installs or updates the program with one command on Linux (and WSL2). It
# downloads a release, checks its SHA256 and starts "omnibox install", which asks the questions
# (Enter keeps the default) or, on an installed computer, updates it.
#
#   gh release download --repo aafnx/omnibox-ai --pattern install.sh --dir /tmp --clobber
#   sudo sh /tmp/install.sh            # add --yes to accept every default, --version 0.18.0 for one version
set -eu

repo=aafnx/omnibox-ai
archive=omnibox-linux-amd64.tar.gz
version=
yes=

while [ $# -gt 0 ]; do
	case "$1" in
	--yes) yes=--yes ;;
	--version) version="v${2#v}"; shift ;;
	*) echo "Неизвестный параметр: $1" >&2; exit 2 ;;
	esac
	shift
done

if [ "$(id -u)" -ne 0 ]; then
	echo "Запустите через sudo." >&2
	exit 1
fi
if [ "$(uname -m)" != x86_64 ]; then
	echo "Нужен 64-битный x86 (amd64)." >&2
	exit 1
fi

temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT

if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
	gh release download $version --repo "$repo" --pattern "$archive" --pattern SHA256SUMS --dir "$temporary" --clobber
else
	# Without gh: a GitHub token typed once, kept only in this shell's memory.
	printf 'GitHub token с доступом на чтение репозитория (ввод скрыт): ' >&2
	stty -echo 2>/dev/null || true
	read -r token
	stty echo 2>/dev/null || true
	echo >&2
	if [ -n "$version" ]; then path="tags/$version"; else path=latest; fi
	release=$(curl -fsSL -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" "https://api.github.com/repos/$repo/releases/$path")
	for name in "$archive" SHA256SUMS; do
		# The asset API URL precedes its name in the release JSON.
		url=$(printf '%s' "$release" | tr ',' '\n' | grep -B3 "\"name\": *\"$name\"" | sed -n 's/.*"url": *"\([^"]*\/assets\/[0-9]*\)".*/\1/p' | head -n1)
		if [ -z "$url" ]; then echo "В выпуске нет файла $name." >&2; exit 1; fi
		# curl does not pass the token on to the storage host GitHub redirects to.
		curl -fsSL -H "Authorization: Bearer $token" -H "Accept: application/octet-stream" -o "$temporary/$name" "$url"
	done
	token=
fi

(cd "$temporary" && grep " \*\?$archive\$" SHA256SUMS | sed 's/ \*/  /' | sha256sum -c --status) || {
	echo "Контрольная сумма выпуска не совпадает; установка отменена." >&2
	exit 1
}
mkdir "$temporary/package"
tar -xzf "$temporary/$archive" -C "$temporary/package"
"$temporary/package/omnibox" install $yes
