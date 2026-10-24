# Contributing to TwoPlacePaste

Thanks for wanting to help. Bug reports, ideas and pull requests are all
welcome.

## Before you start

- **Security issues** go through a private
  [GitHub security advisory](https://github.com/Mark7888/two-place-paste/security/advisories/new),
  not a public issue.
- **For anything bigger than a small fix**, open an issue first so we can agree
  on the approach before you spend time on it.
- Read [`docs/internal/conventions.md`](docs/internal/conventions.md). Reviews
  hold pull requests to it.
- [`docs/internal/SPECS.md`](docs/internal/SPECS.md) is the source of truth for
  behaviour. A change that disagrees with it updates the spec in the same pull
  request, or is a bug.

## Setting up

Build, test and lint commands, the desktop dev loop and the repository layout
are on the documentation site under
[Contributing](https://mark7888.github.io/two-place-paste/contributing/development/).
In short:

```sh
make build test lint      # every Go module
make help                 # everything else
```

Each npm project (`desktop/ui`, `server/ui`, `mobile`, `docs/site`, `proto`) has its own
`npm ci`, and CI runs lint, typecheck, test and build for each of them. Run
the checks for the parts you changed before you push.

## Pull requests

- Keep a pull request to one change. The roadmap in
  [`docs/internal/ROADMAP.md`](docs/internal/ROADMAP.md) is carried out one
  phase per pull request.
- Shared contracts (`proto/`, `spec/`, `go.work`, the root `Makefile`,
  `.golangci.yml`, the CI workflows and the license files) change in their own
  small pull request, opened from an issue tagged `contract-change`, not mixed
  into feature work.
- A change users or operators would notice updates the documentation site in
  `docs/site/` in the same pull request.
- Commit subjects are short, imperative and in sentence case, without a prefix
  (`Fix reconnect after sleep`). The body explains what changed and why.
- Never put clipboard content, keys, tokens or passwords in logs, errors, tests
  or issue reports. The rules are in section 10 of the conventions.

## Licensing of contributions

TwoPlacePaste uses two licenses, described in [`LICENSING.md`](LICENSING.md):

- `server/` is licensed under the **GNU Affero General Public License v3.0 or
  later** ([`server/LICENSE`](server/LICENSE)).
- Everything else is licensed under the **Mozilla Public License 2.0**
  ([`LICENSE`](LICENSE)).

By opening a pull request you agree that your contribution is licensed under
the license of the folder it changes, and that you have the right to submit it
under that license. You keep the copyright on your contribution. There is no
contributor license agreement to sign.

Don't move or copy code between `server/` and the rest of the repository: that
would change its license. If server code is needed elsewhere, raise it in an
issue first.
