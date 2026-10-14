#!/usr/bin/env bash
set -euo pipefail

# Select a fetched commit without creating local branches. A first branch push
# has a zero before-SHA, so compare against the repository's default branch.
if [ -n "${PULL_REQUEST_BASE:-}" ]; then
	baseline_ref="refs/remotes/origin/$PULL_REQUEST_BASE"
elif [ -n "${PUSH_BEFORE:-}" ] && [ "$PUSH_BEFORE" != "0000000000000000000000000000000000000000" ]; then
	if [[ ! "$PUSH_BEFORE" =~ ^[0-9a-fA-F]{40}$ ]]; then
		echo "Push baseline is not a valid commit SHA." >&2
		exit 1
	fi
	baseline_ref="$PUSH_BEFORE"
elif [ -n "${DEFAULT_BRANCH:-}" ]; then
	baseline_ref="refs/remotes/origin/$DEFAULT_BRANCH"
else
	echo "No protobuf compatibility baseline is available for this event." >&2
	exit 1
fi

if ! baseline="$(git rev-parse --verify --quiet "$baseline_ref^{commit}")"; then
	echo "Fetched compatibility baseline was not found: $baseline_ref" >&2
	exit 1
fi
printf 'BUF_BASE_REF=%s\n' "$baseline"
