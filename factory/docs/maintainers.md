# Maintainer guide

This guide records repository policy that is enforced in GitHub rather than in
the source tree.

## Main branch policy

The active `main` branch ruleset is the only branch-protection policy for the
default branch. It applies to GitHub's default branch target so it remains valid
if the branch is renamed.

Every change to `main` must:

- be merged through a pull request;
- pass the `check` status produced by the GitHub Actions app;
- be up to date with `main` before merging; and
- resolve all pull-request review conversations.

The ruleset blocks force pushes and branch deletion. It does not require an
approval because the project currently has one maintainer and GitHub does not
allow a pull-request author to approve their own change.

There is no routine bypass. If a GitHub Actions outage or broken workflow makes
the required check impossible to satisfy, the repository owner may temporarily
disable the `main` ruleset in **Settings > Rules > Rulesets**. Record the reason
on the affected pull request, merge only that reviewed change, and restore the
ruleset immediately after the exceptional merge even if the outage continues.
Verify that it is active before merging any later change. Do not weaken or
disable the policy to merge a failing change.

When the workflow or its required check name changes, update the ruleset and
this guide in the same pull request. Remove superseded branch rules instead of
leaving a second active or disabled policy.
