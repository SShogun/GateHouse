#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
fixture_dir="$(mktemp -d)"
trap 'rm -rf -- "$fixture_dir"' EXIT
git init --quiet --initial-branch=main "$fixture_dir"
cd "$fixture_dir"
git config user.name 'CI baseline test'
git config user.email 'ci-baseline@example.invalid'
printf 'baseline\n' > fixture
git add fixture
git commit --quiet -m baseline
base="$(git rev-parse HEAD)"
printf 'previous push\n' >> fixture
git commit --quiet -am previous
previous="$(git rev-parse HEAD)"
printf 'new branch\n' >> fixture
git commit --quiet -am current
current="$(git rev-parse HEAD)"
git update-ref refs/remotes/origin/main "$base"
git update-ref refs/remotes/origin/release "$previous"
zeros=0000000000000000000000000000000000000000

expect_baseline() {
	local description="$1" expected="$2" pr="$3" before="$4" default="$5" actual
	if ! actual="$(PULL_REQUEST_BASE="$pr" PUSH_BEFORE="$before" DEFAULT_BRANCH="$default" bash "$script_dir/select-proto-baseline.sh" 2>diagnostic)"; then
		printf 'FAIL: %s did not select a compatibility baseline\n' "$description" >&2
		cat diagnostic >&2
		exit 1
	fi
	if [ "$actual" != "BUF_BASE_REF=$expected" ]; then
		printf 'FAIL: %s selected %s; expected BUF_BASE_REF=%s\n' "$description" "$actual" "$expected" >&2
		exit 1
	fi
	printf 'PASS: %s\n' "$description"
}

expect_failure() {
	local description="$1" pr="$2" before="$3" default="$4"
	if PULL_REQUEST_BASE="$pr" PUSH_BEFORE="$before" DEFAULT_BRANCH="$default" bash "$script_dir/select-proto-baseline.sh" >output 2>diagnostic; then
		printf 'FAIL: %s silently accepted a missing or invalid baseline\n' "$description" >&2
		exit 1
	fi
	if [ -s output ]; then
		printf 'FAIL: %s emitted a baseline before rejecting it\n' "$description" >&2
		exit 1
	fi
	printf 'PASS: %s\n' "$description"
}

expect_baseline 'first branch push' "$base" '' "$zeros" main
expect_baseline 'pull request base takes precedence' "$previous" release "$current" main
expect_baseline 'existing branch push uses prior head' "$previous" '' "$previous" main
expect_baseline 'missing before uses default branch' "$base" '' '' main
expect_baseline 'non-main default branch' "$previous" '' "$zeros" release
expect_failure 'missing PR base fails closed' absent "$previous" main
expect_failure 'missing default branch fails closed' '' "$zeros" absent
expect_failure 'unfetched prior head fails closed' '' aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa main
expect_failure 'malformed prior head fails closed' '' '--help' main
expect_failure 'no event baseline fails closed' '' "$zeros" ''
