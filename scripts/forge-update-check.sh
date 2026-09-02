#!/usr/bin/env bash
# Refuse a forge bump that moves the CLI surface ccpm's adapter depends on (G2).
# Exits 0 when the pin is current or the surface is unchanged; 1 when it moved.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
forge_repo="${FORGE_REPO:-$repo_root/../forge}"

# The files the adapter reads through: verbs, the -f encoding, the -o flag, the JSON shapes.
surface=(
  internal/cli/issue.go
  internal/cli/label.go
  internal/cli/comment.go
  internal/cli/api.go
  internal/cli/root.go
  types.go
)

die() { printf '%s\n' "$1" >&2; printf 'next: %s\n' "$2" >&2; exit 2; }

[ -d "$forge_repo/.git" ] ||
  die "forge clone not found at $forge_repo" \
      "git clone https://github.com/git-pkgs/forge \"$forge_repo\", or set FORGE_REPO to an existing clone"

# One source for the pin: .mise.toml's tools entry.
pin="$(sed -n 's|^"go:github.com/git-pkgs/forge[^"]*" *= *"\([^"]*\)".*|\1|p' "$repo_root/.mise.toml" | head -1)"
[ -n "$pin" ] ||
  die "no forge pin in $repo_root/.mise.toml" \
      'add `"go:github.com/git-pkgs/forge/cmd/forge" = "<version>"` under [tools] in .mise.toml'

pinned_ref="v${pin#v}"
git -C "$forge_repo" rev-parse --verify --quiet "$pinned_ref^{commit}" >/dev/null ||
  die "tag $pinned_ref is not in $forge_repo" \
      "git -C \"$forge_repo\" fetch --tags"

latest="${FORGE_LATEST:-$(git -C "$forge_repo" tag -l 'v*' | sort -V | tail -1)}"
[ -n "$latest" ] || die "no v* tags in $forge_repo" "git -C \"$forge_repo\" fetch --tags"

if [ "$pinned_ref" = "$latest" ]; then
  printf 'forge pin %s is current.\n' "$pinned_ref"
  exit 0
fi

if git -C "$forge_repo" diff --quiet "$pinned_ref" "$latest" -- "${surface[@]}"; then
  printf 'forge %s -> %s: CLI surface unchanged, bump is safe.\n' "$pinned_ref" "$latest"
  exit 0
fi

printf 'forge %s -> %s: CLI surface moved. Bump refused.\n\n' "$pinned_ref" "$latest" >&2
git -C "$forge_repo" diff "$pinned_ref" "$latest" -- "${surface[@]}" >&2
printf '\nnext: re-run the adapter contract suite against %s, then raise the pin in .mise.toml\n' "$latest" >&2
exit 1
