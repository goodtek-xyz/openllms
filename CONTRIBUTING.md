# Contributing to openllms

Thanks for helping. Issues and pull requests are welcome here.

## How changes reach this repository

openllms is **published from an upstream source tree**: every commit on `main` and every release tag is a publish of
that tree, after its own review, tests, staging and a secret / internal-detail scan. So pull requests are **not merged
here**. Instead:

1. You open a pull request (or an issue) on this repository.
2. A maintainer reviews it here, then carries the change into the upstream tree, keeping you as the author
   (`Co-authored-by:` with your name and email, and your `Signed-off-by:` line).
3. The change goes through the normal upstream flow: review, CI, staging.
4. When the next publish contains it, the maintainer links that publish commit on your pull request and closes it.

Your contribution is credited in the published history even though the pull request itself is closed, not merged.

## Sign your commits (DCO)

Every commit in a pull request must carry a `Signed-off-by:` line, certifying the
[Developer Certificate of Origin 1.1](https://developercertificate.org/): that you wrote the change or otherwise have
the right to submit it under this project's license (MIT), and that the record of your contribution, sign-off
included, is public and kept.

```bash
git commit -s -m "Fix route preset parsing"
# adds: Signed-off-by: Your Name <you@example.com>  (your git user.name / user.email)
```

The sign-off must use the same email as the commit author. A check on every pull request verifies this; to fix a
missing sign-off, amend or rebase with `-s` and force-push your branch:

```bash
git commit --amend -s --no-edit            # last commit
git rebase --signoff origin/main           # every commit on the branch
```

## Before you open a pull request

- `go test ./...` passes (the OSS build has no build tags).
- New behaviour has a test; time values (timeouts, retries, TTLs) cite the documentation they come from.
- No secrets, tokens or real credentials — not even in tests or examples. Use obvious placeholders.
- One topic per pull request; describe what changes and why.

## Reporting a security issue

Please do not open a public issue for a vulnerability. Email **hello@goodtek.xyz** with the details and we will
reply there.
