# CI on Codr

Codr runs your checks on every push with `codr-runner`, a small program beside the forge (D89). Each job runs in a container
with no network, no extra privileges and a read-only system, and its result appears on the commit and on the pull request.

## Add a job

Commit a file named `.codr/ci.toml`:

```toml
image = "golang:1.23-alpine"     # the container image every job uses
branches = ["main", "release/*"] # optional: run only for pushes to these branches (globs); left out = every branch

[jobs.test]
run = [
  "test -z \"$(gofmt -l .)\"",
  "go vet ./...",
  "go test ./...",
]
timeout_minutes = 10             # optional; the runner has its own upper limit

[jobs.lint]
image = "koalaman/shellcheck-alpine:stable"   # a job may use its own image
run = ["shellcheck scripts/*.sh"]
```

- Each job reports as its own check, `ci/<job name>`: *pending* while it waits or runs, then *success* or *failure*.
- Steps run in order with `sh -e`, so the first failing step ends the job. The log shows each step as `$ step`.
- A file with a mistake (an unknown key, a job with no steps, a bad image name) gives one *error* check named `ci` that says what
  is wrong. No file = no checks.
- Inside the job the code is in `/src`, `HOME` is `/tmp`, and `CI`, `CODR_REPO`, `CODR_BRANCH`, `CODR_SHA` are set.
- There is no network unless the server's administrator allows it, so a job cannot download dependencies. Use an image that already
  has what the job needs, or vendor dependencies in the repository.

## Read the result

On the commit page and on the pull request, every check links to its log. The log address is long and random: anyone with the link
can read it, and the link is only shown to people who can read the repository.

## Require green checks

Repository settings → Protected branches → add the branch with **CI checks must pass**. From then on the branch only moves to a commit
whose checks all passed: a push of an untested commit is refused (test it on another branch first), and a pull request can only be
merged once its latest commit is green.

## For the administrator

The runner is set up once per server and once per repository (an account for the runner, a webhook, its secret). The steps are in
`deploy/README.md` under "Pelari CI".
