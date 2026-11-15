# Conventions for this repo

A public fork of `henrygd/beszel`. It adds an SNMP bridge, and it offers small features to upstream.

- The repo is public. Write no host name, IP address or secret of a private network in a file, a commit or a PR. Use example values, such as `192.0.2.10`.
- Branches: `bridge` is the default branch, and all our work is on it. `main` mirrors upstream, so commit nothing to `main`. Why: an upstream PR must hold only the commits of one feature.
- Put our code in new files. Change an upstream file only when the feature needs it. Why: a new file gives no merge conflict in the sync with upstream.
- Upstream PR: before you write code for upstream, or open a feature request or a PR there, read `docs/agents/upstream.md`.
- Commit subjects use the upstream form: `feat(agent): ...`, `fix(hub): ...`. Use no `Co-Authored-By` line.
- Write all prose in ASD-STE100 Simplified Technical English.
- Commit and push with the `ship` skill.
- Never commit a secret, not even encrypted.
- Issues for this repo live in `.scratch/issues/`. Before you create, move or close an issue, read `~/code/dotfiles/docs/agents/issue-tracker.md`. `.scratch` is a symlink to the vault. `.git/info/exclude` ignores it, because `.gitignore` is an upstream file.
- Use no GitHub Actions, no Copilot and no GitHub issues, wiki, projects or discussions in the fork. Talk to upstream in the issues, discussions and PRs of `henrygd/beszel`.
- Test level: Product. Each change to the logic gets a unit test with mocked input. Run the tests with `make test`. Before the first Go test in a new checkout, worktree or export, run `mkdir -p internal/site/dist && touch internal/site/dist/index.html`. Why: `internal/site/embed.go` needs the folder, `make test` does not make it, and without it the run fails at setup after about 5 minutes. No test needs a real SNMP device: use `.snmprec` files and an in-process hub. Main user paths:
  1. A Target shows in the hub as a system, with fans and temperatures.
  2. A Target that does not answer shows as down.
  3. The `FanLow` alert fires and clears.
  4. The sync script stops when the protocol test fails.
- Domain docs are one `GLOSSARY.md` and `docs/adr/` at the repo root. Before you explore the code, read `docs/agents/domain.md`.
