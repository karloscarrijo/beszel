# Work for upstream

Rules for a feature that we offer to `henrygd/beszel`. Sources: <https://beszel.dev/guide/contributing>, the feature request form and the PR template in `.github/`.

## Order

1. Open a feature request with the upstream form. Fill each section. Done when the request is open.
2. Wait until a maintainer removes the label `needs-review`. Open no PR before that.
3. Make the PR branch from `upstream/main`. Cherry-pick the commits of the one feature from `bridge`. Done when `git log upstream/main..HEAD` shows only those commits.
4. Open the PR with the upstream template: Description, Documentation, Changelog, Screenshots. Link the feature request. Done when the PR is open.
5. Open a docs PR in `henrygd/beszel-docs` when the feature has a setting or a new page, and link it in the PR.

The owner approves each text before it goes to GitHub: a feature request, a PR body, a comment.

## Rules for the code

- One feature in one PR. Keep the PR small: aim for less than 600 changed lines.
- Add Go tests with mocked input. Run `make test`. A reviewer must be able to test with no special hardware.
- Follow an existing pattern, and name it in the PR description.
- Comments in a file that goes to upstream: write the reason as a plain sentence, and name an upstream issue as `(#1361)`. Example: `// trim trailing backslash for Windows devices (#1361)`. Keep the `why:` prefix for the files that stay in the fork. Why: upstream code has no `why:` comment.
- Make a new feature opt-in when it needs hardware or a tool that not all systems have.
- Add no heavy dependency. Use no Cgo and no `sudo`.
- Keep the payload from the agent to the hub small.
- Add a new migration. Do not edit an existing migration.
- Do not commit files in `internal/site/src/locales/`.

## Rules for the PR text

- Title in the upstream form: `feat(agent): ...`, `fix(hub): ...`.
- Add screenshots for a UI change.
- State the AI use in one line of the PR body.
- When a maintainer pushes commits to the PR branch, test again and report the result.
