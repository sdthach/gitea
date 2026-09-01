#!/usr/bin/env python3
"""Seed a Gitea delivery preview instance with fake but coherent test data.

Everything goes through the public APIs — Gitea's /api/v1 and the fork's
/api/delivery/v1 — except environment policy, which has no write endpoint and is
therefore set with psql against the preview database.

  pip install faker
  ./seed.py --token <admin token>

Re-running adds a fresh generation; every name carries a run tag, so nothing collides.
"""

import argparse
import base64
import json
import random
import re
import string
import subprocess
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


def psql(project, sql):
    """Set environment policy, which the delivery API deliberately exposes read-only."""
    out = subprocess.run(
        ["docker", "compose", "-p", project, "exec", "-T", "db",
         "psql", "-U", "gitea", "-d", "gitea", "-v", "ON_ERROR_STOP=1", "-c", sql],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        raise RuntimeError("psql failed: " + out.stderr.strip())
    return out.stdout.strip()


def parse_args(argv=None):
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--server", default="http://127.0.0.1:3001")
    p.add_argument("--token", required=True, help="admin access token")
    p.add_argument("--users", type=int, default=5)
    p.add_argument("--repos", type=int, default=3)
    p.add_argument("--issues", type=int, default=14, help="issues per repository")
    p.add_argument("--releases", type=int, default=4, help="releases per repository")
    p.add_argument("--seed", type=int, default=None, help="fix the RNG so the content repeats")
    p.add_argument("--tag", default=None,
                   help="run tag suffixing every name; defaults to random, so runs never collide")
    p.add_argument("--compose-project", default="gitea-preview")
    p.add_argument("--skip-env-policy", action="store_true",
                   help="leave delivery_environment alone; deploys then never hold for approval")
    p.add_argument("--failure-rate", type=float, default=0.25,
                   help="fraction of deploy workflows that exit 1, so the CI overview is not all green")
    p.add_argument("--wait-approvals", type=int, default=120, metavar="SECONDS",
                   help="how long to wait for the runner to reach the prod gate; 0 skips")
    p.add_argument("-v", "--verbose", action="store_true")
    return p.parse_args(argv)


def set_environment_policy(args):
    psql(args.compose_project, """
        UPDATE delivery_environment SET predecessor='dev',     require_predecessor=true  WHERE repo_id=0 AND name='qa';
        UPDATE delivery_environment SET predecessor='qa',      require_predecessor=true  WHERE repo_id=0 AND name='uat';
        UPDATE delivery_environment SET predecessor='uat',     require_predecessor=true  WHERE repo_id=0 AND name='staging';
        UPDATE delivery_environment SET predecessor='staging', require_predecessor=true,
               approval_policy='others_only', required_approvals=1                       WHERE repo_id=0 AND name='prod';
    """)


def make_users(api, fake, tag, count):
    v1 = "/api/v1"
    users = []
    for i in range(count):
        login = f"{slug(fake.user_name(), 1)}-{tag}{i}"
        api("POST", f"{v1}/admin/users", {
            "username": login, "email": f"{login}@example.test",
            "password": PASSWORD, "full_name": fake.name(), "must_change_password": False,
        }, ok=(201, 422))  # 422 is "already exists": a re-run with the same --tag
        tok = api("POST", f"{v1}/users/{login}/tokens",
                  {"name": f"seed-{tag}{i}", "scopes": ["all"]},
                  basic=(login, PASSWORD), ok=(201,))
        users.append({"login": login, "token": tok["sha1"]})
    return users


def make_repo(api, fake, org, name, users, failure_rate):
    v1, full = "/api/v1", f"{org}/{name}"
    api("POST", f"{v1}/orgs/{org}/repos", {
        "name": name, "description": fake.catch_phrase(), "private": False,
        "auto_init": True, "default_branch": "main", "readme": "Default",
    })
    # The deploy endpoint requires write on the Actions unit; projects back the board.
    api("PATCH", f"{v1}/repos/{full}",
        {"has_actions": True, "has_projects": True, "has_issues": True})
    for u in users:
        api("PUT", f"{v1}/repos/{full}/collaborators/{u['login']}", {"permission": "write"})
    make_workflows(api, full, failure_rate)
    return full


def make_workflows(api, full, failure_rate):
    """A deploy dispatches deploy-<env>.yaml, so the tag must already carry the file (D4)."""
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
        api("POST", f"/api/v1/repos/{full}/contents/.gitea%2Fworkflows%2Fdeploy-{env}.yaml", {
            "content": base64.b64encode(body.encode()).decode(),
            "branch": "main",
            "message": f"ci: add the {env} deploy workflow",
        }, ok=(201, 422))


def make_board(api, fake, full):
    v1 = "/api/v1"
    project = api("POST", f"{v1}/repos/{full}/projects", {
        "title": fake.catch_phrase(), "description": fake.sentence(),
        "template_type": "none", "card_type": "text_only",
    })
    columns = [api("POST", f"{v1}/repos/{full}/projects/{project['id']}/columns",
                   {"title": title})["id"] for title in COLUMNS]
    return project, columns


def make_issues(api, fake, full, labels, milestone, project, columns, users, count, totals):
    v1, epics = "/api/v1", []
    for i in range(count):
        itype = "epic" if i < 2 else random.choice(ISSUE_TYPES[2:])
        issue = api("POST", f"{v1}/repos/{full}/issues", {
            "title": fake.sentence(nb_words=6).rstrip("."),
            "body": fake.paragraph(nb_sentences=4),
            "labels": [labels[itype]],
            "milestone": milestone["id"],
            "assignees": [random.choice(users)["login"]] if random.random() < 0.7 else [],
        })
        totals["issues"] += 1
        if itype == "epic":
            epics.append(issue)
        elif epics and random.random() < 0.6:
            parent = random.choice(epics)  # an epic: label is a board lane key (O4)
            api("POST", f"{v1}/repos/{full}/labels",
                {"name": f"epic:{parent['number']}", "color": "ededed"}, ok=(201, 422))
            api("POST", f"{v1}/repos/{full}/issues/{issue['number']}/labels",
                {"labels": [f"epic:{parent['number']}"]}, ok=(200, 201, 422))
        api("POST", f"{v1}/repos/{full}/projects/{project['id']}/columns/"
                    f"{random.choice(columns)}/issues/{issue['id']}", ok=(200, 201, 204))
        totals["cards"] += 1
        if random.random() < 0.25:
            api("PATCH", f"{v1}/repos/{full}/issues/{issue['number']}", {"state": "closed"})


def promote(api, fake, full, release_count, totals, verbose):
    """Walk each release up the sequence, overriding where no runner has made a cell live."""
    dv1 = "/api/delivery/v1"
    for n in range(release_count):
        for env in ENVIRONMENTS:
            body = {"repo": full, "environment": env, "release_tag": f"v1.{n}.0", "confirm": True}
            try:
                res = api("POST", f"{dv1}/deployments", body, ok=(200, 201)) or {}
            except ApiError as e:
                payload = e.payload if isinstance(e.payload, dict) else {}
                if not payload.get("requires_override_reason"):
                    if verbose:
                        print(f"  deploy v1.{n}.0 -> {env}: {e.status} {e.payload}", file=sys.stderr)
                    break
                # The sequence rule speaking: no runner has made the predecessor live, so
                # bypass it with a reason, which lands on the audit log (E17).
                body["override_reason"] = f"preview seed: {fake.sentence()}"
                try:
                    res = api("POST", f"{dv1}/deployments", body, ok=(200, 201)) or {}
                    totals["overridden"] += 1
                except ApiError as e2:
                    if verbose:
                        print(f"  override v1.{n}.0 -> {env}: {e2.status} {e2.payload}", file=sys.stderr)
                    break
            if res.get("outcome") == "refuse":
                break
            totals["deployments"] += 1
            if not res.get("confirmed", True):
                totals["held"] += 1
                break


def pending_approvals(api):
    rows = api("GET", "/api/delivery/v1/approvals?limit=200") or []
    if isinstance(rows, dict):  # a paged envelope, should the resource ever grow one
        rows = rows.get("data") or []
    return [r for r in rows if r.get("state") == "pending"]


def resolve_approvals(api, fake, users, totals, wait, verbose):
    # The gate holds at task assignment, not at dispatch, so a held deploy only appears
    # once the runner asks for the job (models/actions/task.go).
    dv1, deadline = "/api/delivery/v1", time.monotonic() + wait
    rows = pending_approvals(api)
    while not rows and time.monotonic() < deadline:
        time.sleep(3)
        rows = pending_approvals(api)
    if not rows:
        print("approvals: none pending — is the runner up? "
              "(docker compose --profile runner up -d runner)")
        return
    for i, row in enumerate(rows):
        verb = "approve" if i % 3 else "reject"
        try:
            api("POST", f"{dv1}/approvals/{row['id']}/{verb}", {"comment": fake.sentence()},
                token=users[i % len(users)]["token"], ok=(200, 201))
            totals["approved" if verb == "approve" else "rejected"] += 1
        except ApiError as e:
            if verbose:
                print(f"  {verb} {row['id']}: {e.status} {e.payload}", file=sys.stderr)


def main():
    args = parse_args()
    fake = Faker()
    if args.seed is not None:
        Faker.seed(args.seed)
        random.seed(args.seed)
    # --seed fixes the generated content, not the identities: a repeated tag would collide
    # with the previous run's users, org and repositories.
    tag = args.tag or "".join(random.Random().choices(string.ascii_lowercase + string.digits, k=4))

    api = Api(args.server, args.token, args.verbose)
    v1 = "/api/v1"

    whoami = api("GET", f"{v1}/user")
    if not whoami.get("is_admin"):
        sys.exit("the token's account is not an admin; user and org creation will fail")
    print(f"seeding {args.server} as {whoami['login']} (run tag {tag})")

    if args.skip_env_policy:
        print("environments: policy untouched (--skip-env-policy)")
    else:
        set_environment_policy(args)
        print("environments: prod needs 1 approval from someone other than the deployer, after staging")

    users = make_users(api, fake, tag, args.users)
    print(f"users: {len(users)}, each with an access token")

    org = f"{slug(fake.company(), 2)}-{tag}"
    api("POST", f"{v1}/orgs", {"username": org, "full_name": fake.company(), "visibility": "public"})
    for u in users:
        api("PUT", f"{v1}/orgs/{org}/public_members/{u['login']}", ok=(204, 404))
    print(f"org: {org}")

    totals = {"repos": 0, "issues": 0, "releases": 0, "cards": 0,
              "deployments": 0, "overridden": 0, "held": 0, "approved": 0, "rejected": 0}

    for i in range(args.repos):
        full = make_repo(api, fake, org, f"{slug(fake.bs(), 2)}-{tag}{i}", users, args.failure_rate)
        totals["repos"] += 1

        labels = {}
        for t in ISSUE_TYPES:
            labels[t] = api("POST", f"{v1}/repos/{full}/labels", {
                "name": f"type:{t}", "color": "%06x" % random.randrange(0x1000000),
                "description": f"ccpm work item type {t}",
            })["id"]

        milestone = api("POST", f"{v1}/repos/{full}/milestones", {
            "title": f"{fake.word().capitalize()} {fake.year()}", "description": fake.sentence(),
        })
        project, columns = make_board(api, fake, full)
        make_issues(api, fake, full, labels, milestone, project, columns, users, args.issues, totals)

        for n in range(args.releases):
            api("POST", f"{v1}/repos/{full}/releases", {
                "tag_name": f"v1.{n}.0", "target_commitish": "main",
                "name": f"v1.{n}.0 {fake.word()}", "body": fake.paragraph(nb_sentences=2),
            })
            totals["releases"] += 1

        promote(api, fake, full, args.releases, totals, args.verbose)
        print(f"repo: {full}")

    resolve_approvals(api, fake, users, totals, args.wait_approvals, args.verbose)

    print("\n" + json.dumps(totals, indent=2))
    print(f"\nbrowse: {args.server}/delivery/grid  |  /delivery/board  |  /delivery/timeline")
    print(f"sign in as any seeded user with password {PASSWORD!r}")


if __name__ == "__main__":
    main()
