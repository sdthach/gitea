# Hub preview

An isolated Gitea running the fork, plus a generator for test data. Nothing here touches an
existing instance: its own compose project, its own database volume, `127.0.0.1:3001`.

```sh
make -C ../.. build      # only when the Go code changed
./up.sh                  # prints the URL and an admin token
./seed.py --token <token>
```

`up.sh` copies the repo's already-built `./gitea` into a small Alpine image — nothing compiles
in Docker. `templates/`, `public/` and `options/` are bind-mounted from the repo, so editing a
`.tmpl` and reloading the page is enough; no restart.

`./reset.sh` destroys every volume and brings the stack back empty.

## The data

`seed.py` needs `faker` (`pip install faker`) and drives the public APIs — Gitea's `/api/v1`,
the fork's `/api/deployments/v1` and its `/api/planning/v1`, environment policy included. It
creates one account per role with its own token, an organisation with one team per role, and
per repository: deploy workflows, issue types, a `points` field, sprint-shaped milestones, and
a project board holding a full `epic → feature → {story, bug, spike} → task` hierarchy —
assigned round-robin, scheduled, estimated, pointed, dependency-linked and time tracked through
the planning API — before releases are cut and promoted up `dev → qa → uat → staging → prod`,
resolving the reviews that prod holds. Each repository's board prints as
`/planning/projects/<owner>/<repo>/<project id>`; its roadmap shows the same hierarchy at day
or week scale, and Settings → Capacity shows the two users given an explicit row.

The seven roles are site admin, org owner, repo maintainer, deployer, reviewer, reader and
outsider — the last holding no membership of any kind. `prod`'s bypass allowlist names the
reviewers team, so a read-only reviewer can approve and a deployer holding write cannot.
Accounts, tokens and team ids land in `planning/seed-users.md` (`--accounts-file`), gitignored.

Generated names carry their entity kind as a prefix — `org-`, `user-`, `repo-`, `project-`,
`issue-`, `milestone-`, `release-`. Environments are the exception: whatever `app.ini`'s
`[deployments] DEFAULT_ENVIRONMENTS` names — `dev, qa, uat, staging, prod` here — each gets a
`deploy-<env>.yaml`. Gating is the seeder's choice, not the fork's: each names its own
`depends_on`, and `staging`/`prod` set `releases_only` so prereleases stop at `uat`.

Useful flags: `--repos`, `--epics`, `--releases`, `--users`, `--failure-rate`,
`--wait-approvals`, `--seed`/`--tag` to repeat a run, `--self-test` to dry-run the request plan.

## The runner

`up.sh` registers `gitea/act_runner` and starts it under the `runner` compose profile. It is
not optional if you want to see reviews: the gate holds a deploy at task assignment, so with
no runner asking for jobs, no deploy is ever held and no review row exists.

The runner mounts the host Docker socket. That is fine for a local preview and nowhere else.
