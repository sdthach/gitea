#!/usr/bin/env python3
"""Seed a Gitea delivery preview instance with fake but coherent test data.

Everything goes through the public APIs — Gitea's /api/v1 and the fork's /api/delivery/v1.

  pip install faker
  ./seed.py --token <admin token>
  ./seed.py --self-test            # naming doctests only, no server needed

Re-running adds a fresh generation; every name carries a run tag, so nothing collides.
Generated names carry their entity kind as a prefix, so a preview instance reads as seed
data at a glance. Environments and labels are the exceptions: environments are the ones
app.ini's [delivery] DEFAULT_ENVIRONMENTS names, and each has a deploy-<env>.yaml declaring
it; labels keep the `type:` and `epic:` prefixes the board keys its lanes off.
"""

import argparse
import base64
import doctest
import json
import random
import re
import string
import sys
import time
import urllib.error
import urllib.request

from faker import Faker

# ccpm's issue types (references/issue-types.yaml), so the board's type: lanes line up
# with what the epic sync files.
ISSUE_TYPES = ["initiative", "epic", "story", "task", "spike", "bug"]
COLUMNS = ["Backlog", "Ready", "In progress", "In review", "Done"]
ENVIRONMENTS = ["dev", "qa", "uat", "staging", "prod"]
PASSWORD = "preview1234"

V1 = "/api/v1"
DV1 = "/api/delivery/v1"

# Each role gets one account per --users and one org team, so a preview instance can
# demonstrate the whole permission matrix. Outsiders hold no membership of any kind: the
# repositories must not appear to them at all.
ROLES = [
    # role, org team permission (None: no membership), site admin, org owner
    ("admin", "read", True, False),
    ("owner", "admin", False, True),
    ("maintainer", "admin", False, False),
    ("deployer", "write", False, False),
    ("approver", "read", False, False),
    ("reader", "read", False, False),
    ("outsider", None, False, False),
]


class ApiError(RuntimeError):
    def __init__(self, method, path, status, payload):
        self.status, self.payload = status, payload
        super().__init__(f"{method} {path} -> {status}: {payload}")


class Api:
    def __init__(self, base, token, verbose=False):
        self.base, self.token, self.verbose = base.rstrip("/"), token, verbose

    def __call__(self, method, path, body=None, token=None, basic=None, ok=(200, 201, 204)):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method)
        if basic:  # /users/{name}/tokens refuses token auth; upstream requires basic there
            req.add_header("Authorization", "Basic " + base64.b64encode(
                f"{basic[0]}:{basic[1]}".encode()).decode())
        else:
            req.add_header("Authorization", "token " + (token or self.token))
        req.add_header("Accept", "application/json")
        if data:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req) as r:
                raw, status = r.read(), r.status
        except urllib.error.HTTPError as e:
            raw, status = e.read(), e.code
        if self.verbose:
            print(f"  {method} {path} -> {status}", file=sys.stderr)
        try:
            parsed = json.loads(raw) if raw else None
        except json.JSONDecodeError:
            parsed = raw[:200]
        if status not in ok:
            raise ApiError(method, path, status, parsed)
        return parsed


def slug(text, n=3):
    words = re.findall(r"[a-z0-9]+", text.lower())[:n]
    return "-".join(words) or "item"


def name(kind, *parts):
    """Join an entity kind to the parts identifying it.

    >>> name("org", "acme", "ab12")
    'org-acme-ab12'
    >>> name("user", "deployer", 1, "ab12")
    'user-deployer-1-ab12'
    >>> name("repo", "widget", None, "ab12")
    'repo-widget-ab12'
    """
    return "-".join([kind, *(str(p) for p in parts if p not in (None, ""))])


def title(kind, text):
    """Prefix a human-readable title, which keeps its spaces and capitals.

    >>> title("issue", "Fix the login form")
    'issue-Fix the login form'
    >>> title("milestone", "Sprint 1")
    'milestone-Sprint 1'
    """
    return f"{kind}-{text}"


def release_tag(n):
    """Name a release tag. It is a git ref, so it carries no spaces.

    >>> release_tag(0)
    'release-v1.0.0'
    """
    return f"release-v1.{n}.0"


def parse_args(argv=None):
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--server", default="http://127.0.0.1:3001")
    p.add_argument("--token", help="admin access token")
    p.add_argument("--users", type=int, default=1, help="accounts per role; 7 roles are seeded")
    p.add_argument("--repos", type=int, default=3)
    p.add_argument("--issues", type=int, default=14, help="issues per repository")
    p.add_argument("--releases", type=int, default=4, help="releases per repository")
    p.add_argument("--seed", type=int, default=None, help="fix the RNG so the content repeats")
    p.add_argument("--tag", default=None,
                   help="run tag suffixing every name; defaults to random, so runs never collide")
    p.add_argument("--skip-env-policy", action="store_true",
                   help="leave delivery_environment alone; deploys then never hold for approval")
    p.add_argument("--failure-rate", type=float, default=0.25,
                   help="fraction of deploy workflows that exit 1, so the CI overview is not all green")
    p.add_argument("--wait-approvals", type=int, default=120, metavar="SECONDS",
                   help="how long to wait for the runner to reach the prod gate; 0 skips")
    p.add_argument("--accounts-file", default="planning/seed-users.md",
                   help="where to write the seeded accounts, their roles and their tokens")
    p.add_argument("--self-test", action="store_true", help="run the naming doctests and exit")
    p.add_argument("-v", "--verbose", action="store_true")
    args = p.parse_args(argv)
    if not args.self_test and not args.token:
        p.error("--token is required unless --self-test is given")
    return args


def set_environment_policy(api, approver_team_ids):
    """Give the default set a sequence, and gate prod behind an approval.

    PUT replaces the row, so every field the policy is not changing is resent as it stands.
    Enabling prod's bypass allowlist is what separates an approver from a deployer: with an
    allowlist in force, approval is the allowlist's decision alone, so a deployer holding
    write cannot approve and a read-only approver can.
    """
    rows = api("GET", f"{DV1}/environments?repo_id=0&limit=100") or []
    existing = {r["name"]: r for r in rows}
    policy = {
        "qa": {"predecessor": "dev", "require_predecessor": True},
        "uat": {"predecessor": "qa", "require_predecessor": True},
        "staging": {"predecessor": "uat", "require_predecessor": True,
                    "require_full_release": True},
        "prod": {"predecessor": "staging", "require_predecessor": True,
                 "require_full_release": True,
                 "approval_policy": "others_only", "required_approvals": 1,
                 "enable_bypass_allowlist": True,
                 "bypass_allowlist_team_ids": approver_team_ids},
    }
    for env_name, patch in policy.items():
        env = existing.get(env_name)
        if env is None:
            continue
        body = {
            "name": env["name"],
            "sort_order": env["sort_order"],
            "approval_policy": env["approval_policy"],
            "required_approvals": env["required_approvals"],
        }
        body.update(patch)
        api("PUT", f"{DV1}/environments/{env['id']}", body, ok=(200,))


def make_users(api, fake, tag, per_role):
    """One account per role per --users, each with a token the accounts file records."""
    users = []
    for role, permission, is_admin, is_owner in ROLES:
        for i in range(per_role):
            login = name("user", role, i, tag)
            created = api("POST", f"{V1}/admin/users", {
                "username": login, "email": f"{login}@example.test",
                "password": PASSWORD, "full_name": fake.name(), "must_change_password": False,
            }, ok=(201, 422))  # 422 is "already exists": a re-run with the same --tag
            if not isinstance(created, dict) or "id" not in created:
                created = api("GET", f"{V1}/users/{login}")
            if is_admin:
                # CreateUserOption carries no admin flag; the edit endpoint is the only way.
                api("PATCH", f"{V1}/admin/users/{login}",
                    {"admin": True, "login_name": login, "source_id": 0}, ok=(200,))
            tok = api("POST", f"{V1}/users/{login}/tokens",
                      {"name": name("seed", role, i, tag), "scopes": ["all"]},
                      basic=(login, PASSWORD), ok=(201,))
            users.append({
                "login": login, "id": created["id"], "token": tok["sha1"], "role": role,
                "permission": permission, "is_admin": is_admin, "is_owner": is_owner,
                "group": name("group", role + "s"),
            })
    return users


def find_team(api, org, team_name):
    for team in api("GET", f"{V1}/orgs/{org}/teams?limit=100") or []:
        if team["name"].lower() == team_name.lower():
            return team["id"]
    return None


def make_groups(api, org, users):
    """Create one team per role and place every member. Outsiders join nothing."""
    teams = {}
    for role, permission, _, _ in ROLES:
        group = name("group", role + "s")
        if permission is None:
            teams[group] = None
            continue
        # No units: sending them leaves the team's own access mode at none, and a team is
        # what grants the units anyway.
        created = api("POST", f"{V1}/orgs/{org}/teams", {
            "name": group, "description": f"preview {role} accounts",
            "permission": permission, "includes_all_repositories": True,
            "can_create_org_repo": permission == "admin", "visibility": "public",
        }, ok=(201, 422))
        teams[group] = created["id"] if isinstance(created, dict) and "id" in created \
            else find_team(api, org, group)
    owners = find_team(api, org, "Owners")
    for u in users:
        for team_id in (teams.get(u["group"]), owners if u["is_owner"] else None):
            if team_id:
                api("PUT", f"{V1}/teams/{team_id}/members/{u['login']}", ok=(200, 204, 404))
    return teams


def make_repo(api, fake, org, repo_name, users, failure_rate):
    full = f"{org}/{repo_name}"
    api("POST", f"{V1}/orgs/{org}/repos", {
        "name": repo_name, "description": fake.catch_phrase(), "private": True,
        "auto_init": True, "default_branch": "main", "readme": "Default",
    })
    # The deploy endpoint requires write on the Actions unit; projects back the board.
    api("PATCH", f"{V1}/repos/{full}",
        {"has_actions": True, "has_projects": True, "has_issues": True})
    # Repository administrator is a collaboration, not a team: joining a team writes no
    # access row, and a caller's access mode is raised from a team only for an owner team,
    # so a maintainer team holds admin on every unit and is still not repository admin.
    for u in users:
        if u["role"] == "maintainer":
            api("PUT", f"{V1}/repos/{full}/collaborators/{u['login']}", {"permission": "admin"})
    make_workflows(api, full, failure_rate)
    return full


def make_workflows(api, full, failure_rate):
    """A deploy dispatches deploy-<env>.yaml, so the tag must already carry the file."""
    for env in ENVIRONMENTS:
        # act defaults every run step to bash, which the tiny runner image has not got.
        fail = "\n      - run: exit 1\n" if random.random() < failure_rate else ""
        body = (
            f"name: deploy {env}\n"
            "on:\n"
            "  workflow_dispatch:\n"
            "jobs:\n"
            "  deploy:\n"
            "    runs-on: ubuntu-latest\n"
            f"    environment: {env}\n"
            "    defaults:\n"
            "      run:\n"
            "        shell: sh\n"
            "    steps:\n"
            f"      - run: echo \"deploying to {env}\"{fail}"
        )
        api("POST", f"{V1}/repos/{full}/contents/.gitea%2Fworkflows%2Fdeploy-{env}.yaml", {
            "content": base64.b64encode(body.encode()).decode(),
            "branch": "main",
            "message": f"ci: add the {env} deploy workflow",
        }, ok=(201, 422))


def make_board(api, fake, full):
    project = api("POST", f"{V1}/repos/{full}/projects", {
        "title": title("project", fake.catch_phrase()), "description": fake.sentence(),
        "template_type": "none", "card_type": "text_only",
    })
    columns = [api("POST", f"{V1}/repos/{full}/projects/{project['id']}/columns",
                   {"title": column})["id"] for column in COLUMNS]
    return project, columns


def make_issues(api, fake, full, labels, milestone, project, columns, users, count, totals):
    epics, assignable = [], [u for u in users if u["role"] != "outsider"]
    for i in range(count):
        itype = "epic" if i < 2 else random.choice(ISSUE_TYPES[2:])
        issue = api("POST", f"{V1}/repos/{full}/issues", {
            "title": title("issue", fake.sentence(nb_words=6).rstrip(".")),
            "body": fake.paragraph(nb_sentences=4),
            "labels": [labels[itype]],
            "milestone": milestone["id"],
            "assignees": [random.choice(assignable)["login"]] if random.random() < 0.7 else [],
        })
        totals["issues"] += 1
        if itype == "epic":
            epics.append(issue)
        elif epics and random.random() < 0.6:
            parent = random.choice(epics)  # an epic: label is a board lane key
            api("POST", f"{V1}/repos/{full}/labels",
                {"name": f"epic:{parent['number']}", "color": "ededed"}, ok=(201, 422))
            api("POST", f"{V1}/repos/{full}/issues/{issue['number']}/labels",
                {"labels": [f"epic:{parent['number']}"]}, ok=(200, 201, 422))
        api("POST", f"{V1}/repos/{full}/projects/{project['id']}/columns/"
                    f"{random.choice(columns)}/issues/{issue['id']}", ok=(200, 201, 204))
        totals["cards"] += 1
        if random.random() < 0.25:
            api("PATCH", f"{V1}/repos/{full}/issues/{issue['number']}", {"state": "closed"})


def promote(api, fake, full, release_count, totals, verbose):
    """Walk each release up the sequence, overriding where no runner has made a cell live."""
    for n in range(release_count):
        for env in ENVIRONMENTS:
            body = {"repo": full, "environment": env,
                    "release_tag": release_tag(n), "confirm": True}
            try:
                res = api("POST", f"{DV1}/deployments", body, ok=(200, 201)) or {}
            except ApiError as e:
                payload = e.payload if isinstance(e.payload, dict) else {}
                if not payload.get("requires_override_reason"):
                    if verbose:
                        print(f"  deploy {release_tag(n)} -> {env}: {e.status} {e.payload}",
                              file=sys.stderr)
                    break
                # The sequence rule speaking: no runner has made the predecessor live, so
                # bypass it with a reason, which lands on the audit log.
                body["override_reason"] = f"preview seed: {fake.sentence()}"
                try:
                    res = api("POST", f"{DV1}/deployments", body, ok=(200, 201)) or {}
                    totals["overridden"] += 1
                except ApiError as e2:
                    if verbose:
                        print(f"  override {release_tag(n)} -> {env}: {e2.status} {e2.payload}",
                              file=sys.stderr)
                    break
            if res.get("outcome") == "refuse":
                break
            totals["deployments"] += 1
            if not res.get("confirmed", True):
                totals["held"] += 1
                break


def pending_approvals(api):
    rows = api("GET", f"{DV1}/approvals?limit=200") or []
    if isinstance(rows, dict):  # a paged envelope, should the resource ever grow one
        rows = rows.get("data") or []
    return [r for r in rows if r.get("state") == "pending"]


def resolve_approvals(api, fake, users, totals, wait, verbose):
    # The gate holds at task assignment, not at dispatch, so a held deploy only appears
    # once the runner asks for the job (models/actions/task.go).
    deadline = time.monotonic() + wait
    rows = pending_approvals(api)
    while not rows and time.monotonic() < deadline:
        time.sleep(3)
        rows = pending_approvals(api)
    if not rows:
        print("approvals: none pending — is the runner up? "
              "(docker compose --profile runner up -d runner)")
        return
    # prod's allowlist names the approvers team, so nobody else's decision is accepted.
    approvers = [u for u in users if u["role"] == "approver"] or users
    for i, row in enumerate(rows):
        verb = "approve" if i % 3 else "reject"
        try:
            api("POST", f"{DV1}/approvals/{row['id']}/{verb}", {"comment": fake.sentence()},
                token=approvers[i % len(approvers)]["token"], ok=(200, 201))
            totals["approved" if verb == "approve" else "rejected"] += 1
        except ApiError as e:
            if verbose:
                print(f"  {verb} {row['id']}: {e.status} {e.payload}", file=sys.stderr)


def write_accounts(path, server, org, users, teams):
    rows = "\n".join(
        f"| {u['role']} | `{u['login']}` | `{PASSWORD}` | `{u['token']}` | "
        f"`{u['group']}` | {teams.get(u['group']) or '—'} |"
        for u in users)
    try:
        with open(path, "w", encoding="utf-8") as f:
            f.write(
                f"# Seeded accounts — {server}\n\n"
                f"Organization `{org}`. Every account holds an all-scopes token.\n\n"
                "| Role | Login | Password | Token | Group | Team id |\n"
                "|---|---|---|---|---|---|\n" + rows + "\n\n"
                "Outsiders hold no membership: the seeded repositories must not appear to them.\n")
    except OSError as e:
        print(f"accounts file: {e}", file=sys.stderr)
        return None
    return path


def main():
    args = parse_args()
    if args.self_test:
        sys.exit(bool(doctest.testmod().failed))
    fake = Faker()
    if args.seed is not None:
        Faker.seed(args.seed)
        random.seed(args.seed)
    # --seed fixes the generated content, not the identities: a repeated tag would collide
    # with the previous run's users, org and repositories.
    tag = args.tag or "".join(random.Random().choices(string.ascii_lowercase + string.digits, k=4))

    api = Api(args.server, args.token, args.verbose)

    whoami = api("GET", f"{V1}/user")
    if not whoami.get("is_admin"):
        sys.exit("the token's account is not an admin; user and org creation will fail")
    print(f"seeding {args.server} as {whoami['login']} (run tag {tag})")

    users = make_users(api, fake, tag, args.users)
    print(f"users: {len(users)} across {len(ROLES)} roles, each with an access token")

    org = name("org", slug(fake.company(), 2), tag)
    api("POST", f"{V1}/orgs", {"username": org, "full_name": fake.company(), "visibility": "public"})
    teams = make_groups(api, org, users)
    print(f"org: {org}, groups: {len([t for t in teams.values() if t])}")

    if args.skip_env_policy:
        print("environments: policy untouched (--skip-env-policy)")
    else:
        approver_team = teams.get(name("group", "approvers"))
        set_environment_policy(api, [approver_team] if approver_team else [])
        print("environments: prod needs 1 approval from the approvers group, after staging")

    totals = {"repos": 0, "issues": 0, "releases": 0, "cards": 0,
              "deployments": 0, "overridden": 0, "held": 0, "approved": 0, "rejected": 0}

    for i in range(args.repos):
        full = make_repo(api, fake, org, name("repo", slug(fake.bs(), 2), f"{tag}{i}"),
                         users, args.failure_rate)
        totals["repos"] += 1

        labels = {}
        for t in ISSUE_TYPES:
            # Unprefixed: services/delivery/board.go keys its lanes off "type:" and "epic:".
            labels[t] = api("POST", f"{V1}/repos/{full}/labels", {
                "name": f"type:{t}", "color": "%06x" % random.randrange(0x1000000),
                "description": f"ccpm work item type {t}",
            })["id"]

        milestone = api("POST", f"{V1}/repos/{full}/milestones", {
            "title": title("milestone", f"{fake.word().capitalize()} {fake.year()}"),
            "description": fake.sentence(),
        })
        project, columns = make_board(api, fake, full)
        make_issues(api, fake, full, labels, milestone, project, columns, users, args.issues, totals)

        for n in range(args.releases):
            api("POST", f"{V1}/repos/{full}/releases", {
                "tag_name": release_tag(n), "target_commitish": "main",
                "name": f"{release_tag(n)} {fake.word()}", "body": fake.paragraph(nb_sentences=2),
            })
            totals["releases"] += 1

        promote(api, fake, full, args.releases, totals, args.verbose)
        print(f"repo: {full}")

    resolve_approvals(api, fake, users, totals, args.wait_approvals, args.verbose)

    print("\n" + json.dumps(totals, indent=2))
    written = write_accounts(args.accounts_file, args.server, org, users, teams)
    if written:
        print(f"\naccounts: {written}")
    print(f"browse: {args.server}/delivery/grid  |  /delivery/board  |  /delivery/timeline")
    print(f"sign in as any seeded user with password {PASSWORD!r}")


if __name__ == "__main__":
    main()
