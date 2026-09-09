#!/usr/bin/env bash

set -Eeuo pipefail

readonly repository_url="${1:?usage: resolve-git-ref.sh REPOSITORY_URL GIT_REF}"
readonly git_ref="${2:?usage: resolve-git-ref.sh REPOSITORY_URL GIT_REF}"

git ls-remote "$repository_url" \
    "refs/heads/${git_ref}" "refs/tags/${git_ref}^{}" "refs/tags/${git_ref}" \
    | awk -v branch="refs/heads/${git_ref}" \
        -v peeled="refs/tags/${git_ref}^{}" \
        -v tag="refs/tags/${git_ref}" '
        $2 == peeled { peeled_commit = $1 }
        $2 == branch { branch_commit = $1 }
        $2 == tag { tag_commit = $1 }
        END {
            if (peeled_commit != "") print peeled_commit
            else if (branch_commit != "") print branch_commit
            else if (tag_commit != "") print tag_commit
        }
    '
