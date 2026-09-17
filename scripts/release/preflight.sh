#!/bin/sh
# Pre-tag checklist. Run this before tagging a release.
#
#   scripts/release/preflight.sh v0.1.0
#
# It never tags and never pushes: it only reports whether the tree is in a
# state worth tagging, then prints the two commands for a human to run.
set -eu

fail() {
	printf 'preflight: %s\n' "$1" >&2
	exit 1
}

step() {
	printf '\n==> %s\n' "$1"
}

if [ "$#" -ne 1 ]; then
	fail "usage: scripts/release/preflight.sh vX.Y.Z"
fi
TAG="$1"

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

step "tag format"
case "$TAG" in
v[0-9]*.[0-9]*.[0-9]*) printf '%s looks like a release tag\n' "$TAG" ;;
*) fail "tag must look like vX.Y.Z, got ${TAG}" ;;
esac

step "tag is unused"
if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
	fail "tag ${TAG} already exists"
fi
printf '%s is free\n' "$TAG"

step "working tree is clean"
if [ -n "$(git status --porcelain)" ]; then
	git status --short
	fail "commit or discard the changes above first"
fi
printf 'clean\n'

step "make ci-local"
make ci-local

step "goreleaser check"
if command -v goreleaser >/dev/null 2>&1; then
	goreleaser check
else
	printf 'goreleaser is not installed, skipping the config check\n'
	printf 'install it with: go install github.com/goreleaser/goreleaser/v2@latest\n'
fi

step "ready"
printf 'Everything passed. To cut the release, run:\n\n'
printf '  git tag -a %s -m "%s"\n' "$TAG" "$TAG"
printf '  git push origin %s\n\n' "$TAG"
printf 'Pushing the tag starts .github/workflows/release.yml.\n'
