# Delivery preview

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

`seed.py` needs `faker` (`pip install faker`) and drives the public APIs — Gitea's `/api/v1`
and the fork's `/api/delivery/v1`, environment policy included. It creates one account per
role with its own token, an organisation with one team per role, repositories with deploy
workflows and project boards, labelled and assigned issues, releases, then promotes each
release up `dev → qa → uat → staging → prod` and resolves the approvals that prod holds.

The seven roles are site admin, org owner, repo maintainer, deployer, approver, reader and
outsider — the last holding no membership of any kind. `prod` enables its bypass allowlist
naming the approvers team, which is what makes a read-only approver able to approve and a
deployer holding write unable to. Accounts, tokens and team ids land in
`planning/seed-users.md` (`--accounts-file`), which is gitignored.

Generated names carry their entity kind as a prefix — `org-`, `user-`, `repo-`, `project-`,
`issue-`, `milestone-`, `release-`. Environments and labels do not: the fork parses both as
keys, `deploy-<env>.yaml` declares `environment: prod`, and the board keys its lanes off the
`type:` and `epic:` label prefixes.

Useful flags: `--repos`, `--issues`, `--releases`, `--users` (accounts per role),
`--failure-rate`, `--wait-approvals`, `--seed` to repeat the content, `--tag` to repeat the
identities, `--self-test` to run the naming doctests without a server.

## The runner

`up.sh` registers `gitea/act_runner` and starts it under the `runner` compose profile. It is
not optional if you want to see approvals: the gate holds a deploy at task assignment, so with
no runner asking for jobs, no deploy is ever held and no approval row exists.

The runner mounts the host Docker socket. That is fine for a local preview and nowhere else.
