#!/usr/bin/env python3
"""Seed a Gitea hub preview instance with fake but coherent test data.

Everything goes through the public APIs — Gitea's /api/v1, the fork's /api/deployments/v1 and
its /api/planning/v1.

  pip install faker
  ./seed.py --token <admin token>
  ./seed.py --self-test            # doctests plus a dry run against a stub, no server needed

Re-running adds a fresh generation; every name carries a run tag, so nothing collides.
Generated names carry their entity kind as a prefix, so a preview instance reads as seed
data at a glance. Environments are the exception: they are the ones app.ini's [deployments]
DEFAULT_ENVIRONMENTS names, and each has a deploy-<env>.yaml declaring it. Issue type,
hierarchy, schedule, estimates, points, dependencies, capacity and time tracking all go
through the planning API rather than a label convention.
"""

import argparse
import base64
import doctest
import itertools
import json
import os
import random
import re
import string
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timedelta, timezone

from faker import Faker

# The hierarchy the planning API enforces through RankAllows(parentRank, childRank) —
# services/planning/hierarchy.go — a parent's rank number must be strictly lower than its
# child's. icon names are octicon-* files shipped under public/assets/img/svg.
TYPE_DEFS = [
    # name, rank, icon, color
    ("epic", 1, "octicon-rocket", "#8250df"),
    ("feature", 2, "octicon-package", "#1f6feb"),
    ("story", 3, "octicon-checklist", "#2da44e"),
    ("bug", 3, "octicon-bug", "#d1242f"),
    ("spike", 3, "octicon-beaker", "#bf8700"),
    ("task", 4, "octicon-issue-opened", "#57606a"),
]
LEAF_TYPES = ["story", "bug", "spike"]
LEAF_WEIGHTS = [0.5, 0.3, 0.2]
CLOSE_PROB = {"epic": 0.05, "feature": 0.15, "story": 0.3, "bug": 0.35, "spike": 0.3, "task": 0.4}

COLUMNS = ["Backlog", "Ready", "In progress", "In review", "Done"]
ENVIRONMENTS = ["dev", "qa", "uat", "staging", "prod"]
PASSWORD = "preview1234"

V1 = "/api/v1"
DV1 = "/api/deployments/v1"
PV1 = "/api/planning/v1"

# Each role gets one account per --users and one org team, so a preview instance can
# demonstrate the whole permission matrix. Outsiders hold no membership of any kind: the
# repositories must not appear to them at all.
ROLES = [
    # role, org team permission (None: no membership), site admin, org owner
    ("admin", "read", True, False),
    ("owner", "admin", False, True),
    ("maintainer", "admin", False, False),
    ("deployer", "write", False, False),
    ("reviewer", "read", False, False),
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


class StubApi:
    """Answers every call in memory, so --self-test can run the whole request plan with no
    server: every path and body this module ever sends is exercised for real, just against a
    fake that always succeeds, and records each call for the self-test's own assertions.
    """

    def __init__(self):
        self.calls = []
        self._next_id = 1000

    def __call__(self, method, path, body=None, token=None, basic=None, ok=(200, 201, 204)):
        self.calls.append((method, path, body))
        if method == "GET":
            if path.startswith(f"{V1}/user"):
                return {"is_admin": True, "login": "stub-admin", "id": 1}
            return []
        self._next_id += 1
        resp = {"id": self._next_id}
        if path.endswith("/tokens"):
            resp["sha1"] = f"stub-token-{self._next_id}"
        if method == "POST" and path.endswith("/issues") and "/repos/" in path:
            resp["number"] = self._next_id
        return resp


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


def gen_estimate():
    """A duration the estimate endpoint's parser accepts: hours and minutes only, no day or
    week unit, because modules/util/time_str.go's regex is (\\d+)\\s*([hms]) — "1d" or "3d"
    does not match it and comes back bad_estimate.

    >>> import re
    >>> bool(re.fullmatch(r"\\d+h(\\d+m)?", gen_estimate()))
    True
    """
    hours = random.choice([1, 2, 3, 4, 6, 8, 12, 16, 20, 24, 32])
    minutes = random.choice([0, 15, 30, 45])
    return f"{hours}h{minutes}m" if minutes else f"{hours}h"


def gen_points():
    return random.choice([1, 2, 3, 5, 8, 13])


def parse_args(argv=None):
    p = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--server", default="http://127.0.0.1:3001")
    p.add_argument("--token", help="admin access token")
    p.add_argument("--users", type=int, default=1, help="accounts per role; 7 roles are seeded")
    p.add_argument("--repos", type=int, default=3)
    p.add_argument("--epics", type=int, default=2,
                   help="epics per repository; each grows 2-4 features, each feature 2-5 "
                        "stories/bugs/spikes, some stories gain tasks")
    p.add_argument("--releases", type=int, default=4, help="releases per repository")
    p.add_argument("--seed", type=int, default=None, help="fix the RNG so the content repeats")
    p.add_argument("--tag", default=None,
                   help="run tag suffixing every name; defaults to random, so runs never collide")
    p.add_argument("--skip-env-policy", action="store_true",
                   help="leave deploy_environment alone; deploys then never hold for approval")
    p.add_argument("--failure-rate", type=float, default=0.25,
                   help="fraction of deploy workflows that exit 1, so the CI overview is not all green")
    p.add_argument("--wait-approvals", type=int, default=120, metavar="SECONDS",
                   help="how long to wait for the runner to reach the prod gate; 0 skips")
    p.add_argument("--accounts-file", default="planning/seed-users.md",
                   help="where to write the seeded accounts, their roles and their tokens")
    p.add_argument("--self-test", action="store_true",
                   help="run the doctests and a dry run of the whole request plan, then exit")
    p.add_argument("-v", "--verbose", action="store_true")
    args = p.parse_args(argv)
    if not args.self_test and not args.token:
        p.error("--token is required unless --self-test is given")
    return args


def set_environment_policy(api, reviewer_team_ids):
    """Give the default set a sequence, and gate prod behind an approval.

    PUT replaces the row, so every field the policy is not changing is resent as it stands.
    Enabling prod's bypass allowlist is what separates a reviewer from a deployer: with an
    allowlist in force, approval is the allowlist's decision alone, so a deployer holding
    write cannot approve and a read-only reviewer can.
    """
    rows = api("GET", f"{DV1}/environments?repo_id=0&limit=100") or []
    existing = {r["name"]: r for r in rows}
    policy = {
        "qa": {"depends_on": ["dev"], "require_prior_deployment": True},
        "uat": {"depends_on": ["qa"], "require_prior_deployment": True},
        "staging": {"depends_on": ["uat"], "require_prior_deployment": True,
                    "releases_only": True},
        "prod": {"depends_on": ["staging"], "require_prior_deployment": True,
                 "releases_only": True,
                 "review_policy": "others_only", "required_reviewers": 1,
                 "restrict_reviewers": True,
                 "reviewer_team_ids": reviewer_team_ids},
    }
    for env_name, patch in policy.items():
        env = existing.get(env_name)
        if env is None:
            continue
        body = {
            "name": env["name"],
            "sort_order": env["sort_order"],
            "review_policy": env["review_policy"],
            "required_reviewers": env["required_reviewers"],
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


def seed_capacities(api, users, totals):
    """Two users get an explicit instance-scope capacity row; everyone else resolves to the
    published default.
    """
    candidates = [u for u in users if u["role"] in ("maintainer", "deployer")] or users
    for u in candidates[:2]:
        api("PUT", f"{PV1}/capacity/{u['id']}", {
            "hours_per_day": random.choice([6, 6.5, 7, 8]),
            "utilization": round(random.uniform(0.7, 0.95), 2),
            "workdays": 62,  # Monday through Friday
        }, ok=(200,))
        totals["capacities"] += 1


def make_repo(api, fake, org, repo_name, users, failure_rate):
    full = f"{org}/{repo_name}"
    repo = api("POST", f"{V1}/orgs/{org}/repos", {
        "name": repo_name, "description": fake.catch_phrase(), "private": True,
        "auto_init": True, "default_branch": "main", "readme": "Default",
    })
    # The deploy endpoint requires write on the Actions unit; projects back the board;
    # dependencies must be turned on before any issue can be linked to another.
    api("PATCH", f"{V1}/repos/{full}", {
        "has_actions": True, "has_projects": True, "has_issues": True,
        "internal_tracker": {
            "enable_time_tracker": True,
            "allow_only_contributors_to_track_time": False,
            "enable_issue_dependencies": True,
        },
    })
    # Repository administrator is a collaboration, not a team: joining a team writes no
    # access row, and a caller's access mode is raised from a team only for an owner team,
    # so a maintainer team holds admin on every unit and is still not repository admin.
    for u in users:
        if u["role"] == "maintainer":
            api("PUT", f"{V1}/repos/{full}/collaborators/{u['login']}", {"permission": "admin"})
    make_workflows(api, full, failure_rate)
    return full, repo["id"]


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


def create_issue_types(api, full, repo_id):
    """The types the hierarchy is built from, scoped to this repository."""
    types = {}
    for type_name, rank, icon, color in TYPE_DEFS:
        created = api("POST", f"{PV1}/issue-types", {
            "repo_id": repo_id, "name": type_name, "color": color, "icon": icon, "rank": rank,
        }, ok=(200, 422))
        if not isinstance(created, dict) or "id" not in created:
            rows = api("GET", f"{PV1}/issue-types?repo_id={repo_id}&limit=100") or []
            created = next((t for t in rows if t["name"] == type_name), None)
        types[type_name] = created
    return types


def create_points_field(api, repo_id):
    """A field keyed points must be kind int (services/planning/field.go: pointsKindError),
    since every rollup sums it.
    """
    created = api("POST", f"{PV1}/fields", {
        "repo_id": repo_id, "key": "points", "kind": "int", "label": "Points", "sort": 1,
    }, ok=(200, 422))
    if not isinstance(created, dict) or "id" not in created:
        rows = api("GET", f"{PV1}/fields?repo_id={repo_id}&limit=100") or []
        created = next((f for f in rows if f["key"] == "points"), None)
    return created


def create_milestones(api, fake, full, count=3):
    """Sprint-shaped rows: each due 3-10 weeks out, started 1-3 weeks before its own due date."""
    milestones = []
    for i in range(count):
        due = datetime.now(timezone.utc) + timedelta(weeks=random.uniform(3, 10) + i * 2)
        start = due - timedelta(weeks=random.uniform(1, 3))
        m = api("POST", f"{V1}/repos/{full}/milestones", {
            "title": title("milestone", f"Sprint {i + 1}"),
            "description": fake.sentence(),
            "due_on": due.strftime("%Y-%m-%dT%H:%M:%SZ"),
        })
        api("PUT", f"{PV1}/milestones/{m['id']}/schedule",
            {"repo": full, "start": start.date().isoformat()}, ok=(200,))
        milestones.append(m)
    return milestones


def apply_schedule(api, full, issue_id, totals):
    """60% of issues get a recorded start, 70% a deadline, the deadline always after the
    start when both are sent — PUT .../schedule refuses a start past the issue's own deadline.
    """
    baseline = datetime.now(timezone.utc) - timedelta(days=random.randint(3, 45))
    started = random.random() < 0.6
    start_date = None
    if started:
        start_date = baseline + timedelta(days=random.randint(0, 5))
        api("PUT", f"{PV1}/issues/{issue_id}/schedule",
            {"repo": full, "start": start_date.date().isoformat()}, ok=(200,))
        totals["starts"] += 1
    if random.random() < 0.7:
        deadline = (start_date or baseline) + timedelta(days=random.randint(7, 45))
        api("POST", f"{PV1}/issues/{issue_id}/dates",
            {"repo": full, "end": deadline.date().isoformat()}, ok=(200,))
        totals["deadlines"] += 1
    return started


def apply_estimate_and_points(api, full, issue_id, type_name, totals):
    if random.random() < 0.7:
        api("PUT", f"{PV1}/issues/{issue_id}/estimate",
            {"repo": full, "time_estimate": gen_estimate()}, ok=(200,))
        totals["estimates"] += 1
    if type_name != "epic" and random.random() < 0.75:
        api("PUT", f"{PV1}/issues/{issue_id}/fields",
            {"repo": full, "values": {"points": gen_points()}}, ok=(200,))
        totals["points"] += 1


def pick_column(closed, started):
    if closed:
        return "Done"
    if not started:
        return random.choices(["Backlog", "Ready"], weights=[0.6, 0.4])[0]
    return random.choices(["In progress", "In review"], weights=[0.65, 0.35])[0]


def seed_issue(api, fake, full, type_name, types, milestone_id, assignee, parent_id,
               project_id, columns, totals):
    """Create one issue and carry it through every planning write a card needs: type, parent,
    schedule, estimate, points and a column matching its state.
    """
    issue = api("POST", f"{V1}/repos/{full}/issues", {
        "title": title("issue", fake.sentence(nb_words=6).rstrip(".")),
        "body": fake.paragraph(nb_sentences=4),
        "milestone": milestone_id,
        "assignees": [assignee] if assignee else [],
    })
    totals["issues"] += 1
    api("PUT", f"{PV1}/issues/{issue['id']}/type",
        {"repo": full, "type_id": types[type_name]["id"]}, ok=(200,))
    if parent_id is not None:
        api("PUT", f"{PV1}/issues/{issue['id']}/parent",
            {"repo": full, "parent_issue_id": parent_id}, ok=(200,))
    started = apply_schedule(api, full, issue["id"], totals)
    apply_estimate_and_points(api, full, issue["id"], type_name, totals)
    closed = random.random() < CLOSE_PROB[type_name]
    if closed:
        api("PATCH", f"{V1}/repos/{full}/issues/{issue['number']}", {"state": "closed"})
    column = COLUMNS.index(pick_column(closed, started))
    api("POST", f"{V1}/repos/{full}/projects/{project_id}/columns/{columns[column]}/issues/"
                f"{issue['id']}", ok=(200, 201, 204))
    totals["cards"] += 1
    return {"id": issue["id"], "number": issue["number"], "type": type_name,
            "assignee": assignee, "started": started, "closed": closed}


def link_dependencies(api, full, records, totals):
    """40% of sibling pairs: each later sibling blocked on the one before it."""
    for earlier, later in zip(records, records[1:]):
        if random.random() < 0.4:
            try:
                api("POST", f"{PV1}/issues/{later['id']}/dependencies",
                    {"repo": full, "depends_on_issue_id": earlier["id"]}, ok=(200,))
                totals["dependencies"] += 1
            except ApiError:
                pass  # a re-run with the same --tag can already hold this pair linked


def seed_issue_tree(api, fake, full, types, milestones, assignee_cycle, project_id, columns,
                     epics_count, totals):
    """epic -> feature -> {story, bug, spike} -> task, depth 4 at its deepest, with
    dependencies drawn across each level's own siblings.
    """
    all_records, epics = [], []
    for _ in range(epics_count):
        epic = seed_issue(api, fake, full, "epic", types, random.choice(milestones)["id"],
                          next(assignee_cycle), None, project_id, columns, totals)
        all_records.append(epic)
        epics.append(epic)
        features = []
        for _ in range(random.randint(2, 4)):
            feature = seed_issue(api, fake, full, "feature", types, random.choice(milestones)["id"],
                                 next(assignee_cycle), epic["id"], project_id, columns, totals)
            all_records.append(feature)
            features.append(feature)
            leaves = []
            for _ in range(random.randint(2, 5)):
                leaf_type = random.choices(LEAF_TYPES, weights=LEAF_WEIGHTS)[0]
                leaf = seed_issue(api, fake, full, leaf_type, types, random.choice(milestones)["id"],
                                  next(assignee_cycle), feature["id"], project_id, columns, totals)
                all_records.append(leaf)
                leaves.append(leaf)
                if leaf_type == "story" and random.random() < 0.4:
                    tasks = []
                    for _ in range(random.randint(1, 3)):
                        task = seed_issue(api, fake, full, "task", types,
                                          random.choice(milestones)["id"], next(assignee_cycle),
                                          leaf["id"], project_id, columns, totals)
                        all_records.append(task)
                        tasks.append(task)
                    link_dependencies(api, full, tasks, totals)
            link_dependencies(api, full, leaves, totals)
        link_dependencies(api, full, features, totals)
    link_dependencies(api, full, epics, totals)
    return all_records


def seed_time_entries(api, full, records, token_by_login, totals, stopwatch_state):
    """50% of started issues get 1-3 tracked time entries; the very first one across the
    whole run also gets a stopwatch left running, started as its own assignee.
    """
    for r in records:
        if not r["started"] or not r["assignee"] or random.random() >= 0.5:
            continue
        for _ in range(random.randint(1, 3)):
            api("POST", f"{V1}/repos/{full}/issues/{r['number']}/times",
                {"time": random.choice([1800, 3600, 5400, 7200, 14400]), "user_name": r["assignee"]},
                ok=(200, 201))
        totals["time_entries"] += 1
        if not stopwatch_state["started"]:
            token = token_by_login.get(r["assignee"])
            if token:
                api("POST", f"{V1}/repos/{full}/issues/{r['number']}/stopwatch/start",
                    token=token, ok=(200, 201, 422))
                totals["stopwatches"] += 1
                stopwatch_state["started"] = True


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
                # The sequence rule speaking: no runner has made the dependency live, so
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


def pending_reviews(api):
    rows = api("GET", f"{DV1}/reviews?limit=200") or []
    if isinstance(rows, dict):  # a paged envelope, should the resource ever grow one
        rows = rows.get("data") or []
    return [r for r in rows if r.get("state") == "pending"]


def resolve_reviews(api, fake, users, totals, wait, verbose):
    # The gate holds at task assignment, not at dispatch, so a held deploy only appears
    # once the runner asks for the job (models/actions/task.go).
    deadline = time.monotonic() + wait
    rows = pending_reviews(api)
    while not rows and time.monotonic() < deadline:
        time.sleep(3)
        rows = pending_reviews(api)
    if not rows:
        print("reviews: none pending — is the runner up? "
              "(docker compose --profile runner up -d runner)")
        return
    # prod's allowlist names the reviewers team, so nobody else's decision is accepted.
    reviewers = [u for u in users if u["role"] == "reviewer"] or users
    for i, row in enumerate(rows):
        verb = "approve" if i % 3 else "reject"
        try:
            api("POST", f"{DV1}/reviews/{row['id']}/{verb}", {"comment": fake.sentence()},
                token=reviewers[i % len(reviewers)]["token"], ok=(200, 201))
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
        if os.path.dirname(path):
            os.makedirs(os.path.dirname(path), exist_ok=True)
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


def run(api, fake, args):
    """The whole seed: users, org, then per repository the board, the type/hierarchy/schedule
    planning writes, releases and deployments. Shared by main() against a real server and by
    the self-test's dry run against StubApi.
    """
    tag = args.tag or "".join(random.Random().choices(string.ascii_lowercase + string.digits, k=4))

    whoami = api("GET", f"{V1}/user")
    if not whoami.get("is_admin"):
        sys.exit("the token's account is not an admin; user and org creation will fail")
    print(f"seeding {args.server} as {whoami['login']} (run tag {tag})")

    users = make_users(api, fake, tag, args.users)
    print(f"users: {len(users)} across {len(ROLES)} roles, each with an access token")
    token_by_login = {u["login"]: u["token"] for u in users}

    org = name("org", slug(fake.company(), 2), tag)
    api("POST", f"{V1}/orgs", {"username": org, "full_name": fake.company(), "visibility": "public"})
    teams = make_groups(api, org, users)
    print(f"org: {org}, groups: {len([t for t in teams.values() if t])}")

    if args.skip_env_policy:
        print("environments: policy untouched (--skip-env-policy)")
    else:
        reviewer_team = teams.get(name("group", "reviewers"))
        set_environment_policy(api, [reviewer_team] if reviewer_team else [])
        print("environments: prod needs 1 approval from the reviewers group, after staging")

    totals = {"repos": 0, "issues": 0, "cards": 0, "types_assigned": 0, "parents_set": 0,
              "starts": 0, "deadlines": 0, "estimates": 0, "points": 0, "dependencies": 0,
              "capacities": 0, "time_entries": 0, "stopwatches": 0, "milestones": 0,
              "releases": 0, "deployments": 0, "overridden": 0, "held": 0,
              "approved": 0, "rejected": 0}

    seed_capacities(api, users, totals)
    print(f"capacity: set for {totals['capacities']} users")

    assignable = [u for u in users if u["role"] != "outsider"]
    assignee_cycle = itertools.cycle(u["login"] for u in assignable)
    stopwatch_state = {"started": False}
    project_urls = []

    for i in range(args.repos):
        full, repo_id = make_repo(api, fake, org, name("repo", slug(fake.bs(), 2), f"{tag}{i}"),
                                  users, args.failure_rate)
        totals["repos"] += 1

        types = create_issue_types(api, full, repo_id)
        create_points_field(api, repo_id)
        milestones = create_milestones(api, fake, full)
        totals["milestones"] += len(milestones)
        project, columns = make_board(api, fake, full)

        records = seed_issue_tree(api, fake, full, types, milestones, assignee_cycle,
                                  project["id"], columns, args.epics, totals)
        totals["types_assigned"] += len(records)
        totals["parents_set"] += sum(1 for r in records if r["type"] != "epic")
        seed_time_entries(api, full, records, token_by_login, totals, stopwatch_state)

        for n in range(args.releases):
            api("POST", f"{V1}/repos/{full}/releases", {
                "tag_name": release_tag(n), "target_commitish": "main",
                "name": f"{release_tag(n)} {fake.word()}", "body": fake.paragraph(nb_sentences=2),
            })
            totals["releases"] += 1

        promote(api, fake, full, args.releases, totals, args.verbose)
        print(f"repo: {full} — {len(records)} issues across {len(types)} types")
        project_urls.append(f"{args.server}/planning/projects/{full}/{project['id']}")

    resolve_reviews(api, fake, users, totals, args.wait_approvals, args.verbose)

    print("\n" + json.dumps(totals, indent=2))
    if args.accounts_file:
        written = write_accounts(args.accounts_file, args.server, org, users, teams)
        if written:
            print(f"\naccounts: {written}")
    for url in project_urls:
        print(f"project: {url}")
    print(f"browse: {args.server}/deployments  and  {args.server}/planning/projects")
    print(f"sign in as any seeded user with password {PASSWORD!r}")


# Every planning-API call the self-test's dry run must observe at least once, so a rewrite
# that stops sending one of these fails loudly instead of silently.
REQUIRED_CALLS = [
    ("POST", re.compile(r"^" + re.escape(PV1) + r"/issue-types$")),
    ("POST", re.compile(r"^" + re.escape(PV1) + r"/fields$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/type$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/parent$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/schedule$")),
    ("POST", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/dates$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/estimate$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/fields$")),
    ("POST", re.compile(r"^" + re.escape(PV1) + r"/issues/\d+/dependencies$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/milestones/\d+/schedule$")),
    ("PUT", re.compile(r"^" + re.escape(PV1) + r"/capacity/\d+$")),
    ("POST", re.compile(r"^" + re.escape(V1) + r"/repos/[^/]+/[^/]+/issues/\d+/times$")),
    ("POST", re.compile(r"^" + re.escape(V1) + r"/repos/[^/]+/[^/]+/issues/\d+/stopwatch/start$")),
]

# Body keys the API contract requires as JSON numbers. A str here would still be accepted by
# some of these endpoints (capacity's own doc says so explicitly) but not all of them, and
# sending a number is the contract regardless.
NUMERIC_BODY_KEYS = {
    "type_id", "parent_issue_id", "depends_on_issue_id", "milestone_id", "repo_id", "org_id",
    "column_id", "project_id", "rank", "sort", "hours_per_day", "utilization", "workdays",
    "time",
}


def missing_calls(calls):
    hit = set()
    for method, path, _ in calls:
        for i, (want_method, pattern) in enumerate(REQUIRED_CALLS):
            if method == want_method and pattern.match(path):
                hit.add(i)
    return [f"{m} {p.pattern}" for i, (m, p) in enumerate(REQUIRED_CALLS) if i not in hit]


def non_numeric_values(calls):
    bad = []
    for method, path, body in calls:
        if not isinstance(body, dict):
            continue
        for key, value in body.items():
            if key in NUMERIC_BODY_KEYS and isinstance(value, str):
                bad.append(f"{method} {path}: {key}={value!r} is a string, not a number")
        values = body.get("values")
        if isinstance(values, dict) and isinstance(values.get("points"), str):
            bad.append(f"{method} {path}: values.points={values['points']!r} is a string, not a number")
    return bad


def self_test():
    doctest_failures = doctest.testmod().failed
    if doctest_failures:
        return 1

    # Fixed seed: the dry run must deterministically exercise every required call, not pass
    # or fail depending on which way the dice fell this run.
    Faker.seed(7)
    random.seed(7)
    fake = Faker()
    api = StubApi()
    args = argparse.Namespace(
        server="http://stub.invalid", token="stub", users=1, repos=1, epics=3, releases=1,
        seed=7, tag="selftest", skip_env_policy=False, failure_rate=0.25, wait_approvals=0,
        accounts_file=None, verbose=False,
    )
    run(api, fake, args)

    missing = missing_calls(api.calls)
    bad_numbers = non_numeric_values(api.calls)
    if missing:
        print("self-test: never called:\n  " + "\n  ".join(missing), file=sys.stderr)
    if bad_numbers:
        print("self-test: numbers sent as strings:\n  " + "\n  ".join(bad_numbers), file=sys.stderr)
    if missing or bad_numbers:
        return 1
    print(f"self-test: {len(api.calls)} calls dry-run, every required planning write seen, "
          f"all numbers sent as JSON numbers")
    return 0


def main():
    args = parse_args()
    if args.self_test:
        sys.exit(self_test())
    fake = Faker()
    if args.seed is not None:
        Faker.seed(args.seed)
        random.seed(args.seed)
    api = Api(args.server, args.token, args.verbose)
    run(api, fake, args)


if __name__ == "__main__":
    main()
