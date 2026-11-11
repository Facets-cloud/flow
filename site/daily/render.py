#!/usr/bin/env python3
"""site/daily/render.py — write "How flow is built, with flow" from ~/.flow.

Reads a flow root (flow.db + tasks/<slug>/ directories) READ-ONLY and emits
    <out>/built-with-flow.html      an HTML fragment (dark-theme tokens from site/styles.css)
    <out>/data/this-week.json       the same numbers as data

Privacy is an allowlist, not redaction:
  * only tasks whose project_slug is in PROJECT_SLUGS are ever selected;
  * tasks tagged `private` and tasks whose slug carries a denylist token are dropped;
  * the page carries no free text — only slugs, statuses, counts, PR/issue
    numbers that resolve to Facets-cloud/flow, and phrases from fixed tables;
  * the run refuses to start without the denylist and refuses to emit output
    that contains any denylist token, absolute path, home directory or email.
Fail closed: if any check fails nothing is written, the exit code is non-zero
and stderr names the failing check only (never the matched text).

Stdlib only. Exit codes: 0 ok, 1 runtime error, 2 a leak check failed.
"""
import argparse
import html
import json
import os
import re
import subprocess
import sys
import urllib.parse
from datetime import datetime, timedelta, timezone

import sqlite3

PROJECT_SLUGS = ("flow",)                # allowlist; flow-p2p joins when it is public
REPO = "Facets-cloud/flow"
REPO_URL = "https://github.com/" + REPO
DEFAULT_DAYS = 7
TABLE_CAP = 12                           # rows in the task table; counts cover the full set

# ---- fixed vocabularies ------------------------------------------------------
# Dead auto-runs are classified by matching the log against fixed patterns; the
# page shows the phrase from this table, never a line from the log.
DEAD_PHRASES = {
    "login": "waited on a login",
    "network": "network unreachable",
    "rate_limit": "rate limited",
    "no_log": "no log written",
    "exited": "exited without closing",
    "unclassified": "unclassified",
}
DEAD_PATTERNS = (
    (re.compile(r"not logged in|please run /login|auth login|setup-token|login required", re.I), "login"),
    (re.compile(r"can't reach the api|cannot reach the api|ENOTFOUND|ECONNREFUSED|ETIMEDOUT|network is unreachable", re.I), "network"),
    (re.compile(r"rate.?limit|\b429\b|overloaded", re.I), "rate_limit"),
    (re.compile(r"finished: dead", re.I), "exited"),
)
STATUS_WORDS = {"backlog": "backlog", "in-progress": "in progress", "done": "done"}
AUTO_WORDS = {None: "", "": "", "running": "running", "completed": "completed", "dead": "dead"}

# ---- scanners used by the leak checks ----------------------------------------
REF_RE = re.compile(r"(?<![0-9&#])#(\d{1,5})\b(?![0-9a-fA-F])")
REPO_MENTION_RE = re.compile(re.escape(REPO) + r"(?![A-Za-z0-9_-])", re.I)
# A gh-resolved number also needs a PR/issue cue on its line and no sign that
# the line talks about some other repository (sibling flow-* repos included).
REF_CUE_RE = re.compile(r"\bPRs?\b|\bpull\b|\bissues?\b|\bmerged?\b|\bfix(?:es|ed)?\b|\bcloses?\b|\bopened\b", re.I)
OTHER_REPO_RE = re.compile(
    r"github\.com/(?!" + re.escape(REPO) + r"(?![A-Za-z0-9_-]))"
    r"|[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+#\d"
    r"|\bflow-(?:connectors|mesh|p2p|workspace|desk)\b|\bflowhub\b", re.I)


def mentions_other_repo(text):
    """True when the text points at a repository other than Facets-cloud/flow."""
    return bool(OTHER_REPO_RE.search(REPO_MENTION_RE.sub(" ", text)))


ABS_PATH_RE = re.compile(r"(?<![\w:/.-])(?:/(?:Users|home|private|tmp|var|etc|opt|root|srv|mnt|Volumes)/|~/)")
EMAIL_RE = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")
SECRET_RE = re.compile(r"\b(?:xox[abprs]-|ghp_|ghs_|gho_|sk-[A-Za-z0-9]|AKIA[0-9A-Z]{12}|-----BEGIN)")


class CheckFailed(Exception):
    def __init__(self, name):
        super().__init__(name)
        self.name = name


class Denylist:
    """Case-insensitive tokens. A token hits when it appears in the text and is
    not glued to a preceding letter/digit (so `sk-` fires on `sk-abc` and
    `key sk-x` but not on `task-list`)."""

    def __init__(self, tokens):
        toks = sorted({t.strip().lower() for t in tokens if t.strip() and not t.lstrip().startswith("#")})
        self.tokens = toks
        self._res = [re.compile(r"(?<![a-z0-9])" + re.escape(t), re.I) for t in toks]

    @classmethod
    def load(cls, path):
        if not path or not os.path.isfile(path):
            raise CheckFailed("denylist_present")
        with open(path, encoding="utf-8", errors="replace") as f:
            dl = cls(f.read().splitlines())
        if not dl.tokens:
            raise CheckFailed("denylist_present")
        return dl

    def hit(self, text):
        return any(r.search(text) for r in self._res)


# ---- data access ---------------------------------------------------------------
def load_queries(path):
    out, name, buf = {}, None, []
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = re.match(r"--\s*name:\s*(\S+)", line)
            if m:
                if name:
                    out[name] = "".join(buf)
                name, buf = m.group(1), []
            elif name is not None and not line.lstrip().startswith("--"):
                buf.append(line)
    if name:
        out[name] = "".join(buf)
    return out


def open_ro(path):
    if not os.path.isfile(path):
        raise CheckFailed("db_present")
    uri = "file:" + urllib.parse.quote(os.path.abspath(path)) + "?mode=ro"
    con = sqlite3.connect(uri, uri=True)
    con.row_factory = sqlite3.Row
    return con


def run_query(con, queries, name):
    sql = queries[name]
    n = sql.count("{projects}")
    sql = sql.replace("{projects}", ",".join("?" * len(PROJECT_SLUGS)))
    return con.execute(sql, list(PROJECT_SLUGS) * n).fetchall()


def parse_ts(s):
    if not s:
        return None
    try:
        d = datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None
    if d.tzinfo is None:
        d = d.replace(tzinfo=timezone.utc)
    return d


def safe_listdir(path):
    try:
        return sorted(os.listdir(path))
    except OSError:
        return []


def read_text(path, limit=200_000):
    try:
        with open(path, encoding="utf-8", errors="replace") as f:
            return f.read(limit)
    except OSError:
        return ""


# ---- GitHub reference resolution ---------------------------------------------
class RefResolver:
    """A #NNNN is kept only when its line names Facets-cloud/flow, or when
    `gh` confirms the number exists in that repo. Results are cached outside
    the repo."""

    def __init__(self, use_gh, cache_path):
        self.use_gh = use_gh
        self.cache_path = cache_path
        self.cache = {}
        if cache_path and os.path.isfile(cache_path):
            try:
                self.cache = json.loads(read_text(cache_path) or "{}")
            except ValueError:
                self.cache = {}

    def refs_in(self, text, allow_resolve=True):
        found = set()
        for line in text.splitlines():
            names_repo = bool(REPO_MENTION_RE.search(line))
            may_resolve = allow_resolve and bool(REF_CUE_RE.search(line)) and not mentions_other_repo(line)
            for m in REF_RE.finditer(line):
                n = int(m.group(1))
                if names_repo or (may_resolve and self.resolves(n)):
                    found.add(n)
        return found

    def resolves(self, n):
        if not self.use_gh:
            return False
        key = str(n)
        if key in self.cache:
            return bool(self.cache[key])
        ok = False
        try:
            r = subprocess.run(
                ["gh", "api", "repos/%s/issues/%d" % (REPO, n), "--jq", ".number"],
                capture_output=True, text=True, timeout=20)
            ok = r.returncode == 0 and r.stdout.strip() == str(n)
        except (OSError, subprocess.TimeoutExpired):
            ok = False
        self.cache[key] = ok
        return ok

    def save(self):
        if not self.cache_path:
            return
        try:
            os.makedirs(os.path.dirname(self.cache_path), exist_ok=True)
            with open(self.cache_path, "w") as f:
                json.dump(self.cache, f, sort_keys=True)
        except OSError:
            pass


# ---- collection ------------------------------------------------------------------
def classify_dead(log_text):
    if not log_text.strip():
        return "no_log"
    for rx, key in DEAD_PATTERNS:
        if rx.search(log_text):
            return key
    return "unclassified"


def has_signal(t):
    """A backlog task that never ran and left no notes says nothing; it is
    counted but never gets a table row."""
    return not (t["status"] == "backlog" and not t["auto_run_status"] and t["notes"] == 0)


def choose_rows(tasks):
    """Pick at most TABLE_CAP tasks for the table: every task with a dead run
    or a resolved ref first (dead runs ahead of refs, then most recently
    updated first), then the most recently updated done tasks until the cap.
    Counts are never affected."""
    newest = lambda t: t["_updated"]
    candidates = [t for t in tasks if has_signal(t)]
    first = sorted((t for t in candidates if t["refs"] or t["auto_run_status"] == "dead"), key=newest, reverse=True)
    first.sort(key=lambda t: t["auto_run_status"] != "dead")      # stable: dead rows first
    chosen = first[:TABLE_CAP]
    picked = {t["slug"] for t in chosen}
    rest = sorted((t for t in candidates if t["slug"] not in picked and t["status"] == "done"), key=newest, reverse=True)
    chosen += rest[:max(0, TABLE_CAP - len(chosen))]
    return chosen


def collect(root, con, queries, denylist, now, days, resolver):
    since = now - timedelta(days=days)
    private = {r["task_slug"] for r in run_query(con, queries, "private_tags")}
    rows = run_query(con, queries, "scoped_tasks")

    eligible = {}         # every allowed-project task that passes the privacy filters
    for r in rows:
        slug = r["slug"]
        if slug in private or denylist.hit(slug):
            continue
        eligible[slug] = r

    visible = []          # eligible AND created inside the window
    for slug, r in eligible.items():
        created = parse_ts(r["created_at"])
        if created is None or created < since or created > now + timedelta(hours=1):
            continue
        visible.append(r)
    visible.sort(key=lambda r: r["created_at"], reverse=True)

    # bus traffic
    between = 0
    asks = 0
    bodies_from = {}
    for m in run_query(con, queries, "scoped_messages"):
        created = parse_ts(m["created_at"])
        if created is None or created < since or created > now + timedelta(hours=1):
            continue
        src = m["from_task_slug"]
        if src not in eligible:
            continue
        if m["to_task_slug"]:
            if m["to_task_slug"] in eligible:
                between += 1
                bodies_from.setdefault(src, []).append(m["body"] or "")
        elif m["to_assignee"] == "user" and not m["fyi"]:
            asks += 1
            bodies_from.setdefault(src, []).append(m["body"] or "")

    tasks_out = []
    leaves = {}
    total_notes = 0
    for r in visible:
        slug = r["slug"]
        tdir = os.path.join(root, "tasks", slug)
        updates = [n for n in safe_listdir(os.path.join(tdir, "updates")) if n.endswith(".md")]
        logs = safe_listdir(os.path.join(tdir, "auto-runs"))
        total_notes += len(updates)

        texts = [read_text(os.path.join(tdir, "updates", n)) for n in updates]
        texts += bodies_from.get(slug, [])
        # A task that talks about any other repository keeps only the refs
        # that name Facets-cloud/flow on their own line.
        allow_resolve = not any(mentions_other_repo(t) for t in texts)
        refs = set()
        for t in texts:
            refs |= resolver.refs_in(t, allow_resolve)

        dead_reason = None
        if r["auto_run_status"] == "dead":
            log_text = read_text(os.path.join(tdir, "auto-runs", logs[-1])) if logs else ""
            dead_reason = DEAD_PHRASES[classify_dead(log_text)]

        # Directories and brief.md are named; any other file is counted under
        # "other files" so one-off filenames never reach the page.
        other_files = 0
        for name in safe_listdir(tdir):
            p = os.path.join(tdir, name)
            if "/" in name or ".." in name or name.startswith("."):
                continue
            if os.path.isdir(p):
                label = name + "/"
            elif name == "brief.md":
                label = name
            else:
                other_files += 1
                continue
            if denylist.hit(label):
                continue
            e = leaves.setdefault(label, {"name": label, "tasks": 0, "items": 0})
            e["tasks"] += 1
            if os.path.isdir(p):
                e["items"] += len([x for x in safe_listdir(p) if not x.startswith(".")])
        if other_files:
            e = leaves.setdefault("other files", {"name": "other files", "tasks": 0, "items": 0})
            e["tasks"] += 1
            e["items"] += other_files

        tasks_out.append({
            "slug": slug,
            "status": r["status"],
            "auto_run_status": r["auto_run_status"] or None,
            "child": r["parent_slug"] is not None,
            "notes": len(updates),
            "refs": sorted(refs),
            "dead_reason": dead_reason,
            "shown": False,
            "_updated": r["updated_at"] or r["created_at"] or "",
        })
    for t in choose_rows(tasks_out):
        t["shown"] = True
    for t in tasks_out:
        del t["_updated"]

    counts = {
        "tasks": len(tasks_out),
        "children": sum(1 for t in tasks_out if t["child"]),
        "done": sum(1 for t in tasks_out if t["status"] == "done"),
        "dead": sum(1 for t in tasks_out if t["auto_run_status"] == "dead"),
        "running": sum(1 for t in tasks_out if t["auto_run_status"] == "running"),
        "messages_between_tasks": between,
        "asks_to_human": asks,
        "notes": total_notes,
        "refs": len({n for t in tasks_out for n in t["refs"]}),
    }
    dead_reasons = {}
    for t in tasks_out:
        if t["dead_reason"]:
            dead_reasons[t["dead_reason"]] = dead_reasons.get(t["dead_reason"], 0) + 1

    return {
        "generated_at": now.isoformat(timespec="seconds"),
        "window_days": days,
        "projects": list(PROJECT_SLUGS),
        "repo": REPO,
        "counts": counts,
        "dead_reasons": dead_reasons,
        "table_cap": TABLE_CAP,
        "shown": sum(1 for t in tasks_out if t["shown"]),
        "tasks": tasks_out,
        "leaves_behind": sorted(leaves.values(), key=lambda e: (-e["tasks"], e["name"])),
    }


# ---- rendering --------------------------------------------------------------------
CSS = """
<style>
.bwf .bwf-stats{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin:20px 0 28px}
@media (max-width:640px){.bwf .bwf-stats{grid-template-columns:repeat(2,1fr)}}
.bwf .bwf-stat{background:var(--bg-paper);border:1px solid var(--rule-soft);border-radius:var(--r-md);padding:14px 16px}
.bwf .bwf-stat__n{display:block;font:600 28px/1.1 var(--font-mono);color:var(--fg)}
.bwf .bwf-stat__l{display:block;margin-top:6px;font:13px/1.3 var(--font-sans);color:var(--fg-muted)}
.bwf table{width:100%;border-collapse:collapse;font:13px/1.5 var(--font-mono);color:var(--fg-soft)}
.bwf th{text-align:left;font-weight:500;color:var(--fg-muted);border-bottom:1px solid var(--rule);padding:6px 8px}
.bwf td{border-bottom:1px solid var(--rule-soft);padding:6px 8px;vertical-align:top}
.bwf .bwf-slug{color:var(--fg)}
.bwf .bwf-child{color:var(--fg-dim)}
.bwf .bwf-dead{color:var(--term-red)}
.bwf .bwf-done{color:var(--term-green)}
.bwf .bwf-run{color:var(--term-amber)}
.bwf a{color:var(--grad-3);text-decoration:none}
.bwf a:hover{text-decoration:underline}
.bwf .bwf-foot{margin-top:18px;font:12px/1.5 var(--font-sans);color:var(--fg-dim)}
.bwf ul.bwf-leaves{list-style:none;padding:0;margin:8px 0 0;font:13px/1.7 var(--font-mono);color:var(--fg-soft)}
</style>
"""

STAT_LABELS = (
    ("tasks", "tasks created"),
    ("children", "spawned as children"),
    ("done", "closed with flow done"),
    ("dead", "children died"),
    ("messages_between_tasks", "messages between tasks"),
    ("asks_to_human", "asks to the human"),
    ("notes", "progress notes left"),
)


def esc(s):
    return html.escape(str(s), quote=True)


def status_cell(t):
    cls = {"done": "bwf-done", "in-progress": "bwf-run", "backlog": ""}.get(t["status"], "")
    return '<span class="%s">%s</span>' % (cls, esc(STATUS_WORDS.get(t["status"], t["status"])))


def auto_cell(t):
    a = t["auto_run_status"]
    word = AUTO_WORDS.get(a, "")
    if not word:
        return '<span class="bwf-child">tab session</span>'
    if a == "dead":
        reason = t["dead_reason"] or DEAD_PHRASES["unclassified"]
        return '<span class="bwf-dead">child died: %s</span>' % esc(reason)
    cls = "bwf-run" if a == "running" else ""
    return '<span class="%s">%s</span>' % (cls, esc(word))


def refs_cell(t):
    if not t["refs"]:
        return '<span class="bwf-child">&mdash;</span>'
    return " ".join('<a href="%s/issues/%d">#%d</a>' % (REPO_URL, n, n) for n in t["refs"])


def render_html(data):
    c = data["counts"]
    parts = [CSS]
    parts.append('<section id="built-with-flow" class="brief brief--alt bwf" aria-labelledby="bwf-heading">')
    parts.append('  <div class="brief__inner">')
    parts.append('    <h2 id="bwf-heading" class="brief__title">How flow is built, with flow</h2>')
    parts.append('    <p class="section__lede">The last %d days of the <code>flow</code> project, read straight out of the '
                 'flow database by a flow playbook. Slugs, statuses and counts only; no brief, note or message text '
                 'ever reaches this page.</p>' % data["window_days"])
    parts.append('    <div class="bwf-stats">')
    for key, label in STAT_LABELS:
        parts.append('      <div class="bwf-stat"><span class="bwf-stat__n" data-count="%s">%d</span>'
                     '<span class="bwf-stat__l">%s</span></div>' % (key, c[key], esc(label)))
    parts.append('    </div>')
    parts.append('    <table>')
    parts.append('      <thead><tr><th>task</th><th>status</th><th>headless run</th><th>notes</th><th>refs</th></tr></thead>')
    parts.append('      <tbody>')
    for t in data["tasks"]:
        if not t["shown"]:
            continue
        marker = ' <span class="bwf-child">child</span>' if t["child"] else ""
        parts.append('        <tr><td><span class="bwf-slug">%s</span>%s</td><td>%s</td><td>%s</td>'
                     '<td>%d</td><td>%s</td></tr>' % (esc(t["slug"]), marker, status_cell(t), auto_cell(t),
                                                      t["notes"], refs_cell(t)))
    parts.append('      </tbody>')
    parts.append('    </table>')
    more = data["counts"]["tasks"] - data["shown"]
    if more > 0:
        parts.append('    <p class="bwf-foot bwf-more" data-more="%d">and %d more %s this week</p>'
                     % (more, more, "task" if more == 1 else "tasks"))
    if data["dead_reasons"]:
        parts.append('    <p class="bwf-foot">Why children died: ' + ", ".join(
            "%s &times;%d" % (esc(k), v) for k, v in sorted(data["dead_reasons"].items())) + '.</p>')
    parts.append('    <h3 class="card__title">What a task leaves behind</h3>')
    parts.append('    <ul class="bwf-leaves">')
    for e in data["leaves_behind"]:
        extra = " (%d inside)" % e["items"] if (e["name"].endswith("/") or e["name"] == "other files") else ""
        parts.append('      <li>%s &times;%d%s</li>' % (esc(e["name"]), e["tasks"], esc(extra)))
    parts.append('    </ul>')
    parts.append('    <p class="bwf-foot">Generated %s by the <code>site-daily</code> playbook. The queries are in '
                 '<a href="%s/blob/main/site/daily/queries.sql">site/daily/queries.sql</a>; the renderer and its leak '
                 'tests in <a href="%s/tree/main/site/daily">site/daily</a>.</p>'
                 % (esc(data["generated_at"][:10]), REPO_URL, REPO_URL))
    parts.append('  </div>')
    parts.append('</section>')
    return "\n".join(parts) + "\n"


# ---- leak checks ----------------------------------------------------------------
def run_checks(html_text, json_text, data, denylist):
    blob = html_text + "\n" + json_text
    passed = []
    if denylist.hit(blob):
        raise CheckFailed("denylist_scan")
    passed.append("denylist_scan")
    if ABS_PATH_RE.search(blob):
        raise CheckFailed("absolute_path_scan")
    passed.append("absolute_path_scan")
    home = os.path.expanduser("~")
    if home and home != "~" and home in blob:
        raise CheckFailed("home_dir_scan")
    passed.append("home_dir_scan")
    if EMAIL_RE.search(blob):
        raise CheckFailed("email_scan")
    passed.append("email_scan")
    if SECRET_RE.search(blob):
        raise CheckFailed("secret_pattern_scan")
    passed.append("secret_pattern_scan")
    for key, _ in STAT_LABELS:
        m = re.search(r'data-count="%s">(\d+)<' % key, html_text)
        if not m or int(m.group(1)) != data["counts"][key]:
            raise CheckFailed("json_html_parity")
    if len(re.findall(r'class="bwf-slug"', html_text)) != data["shown"]:
        raise CheckFailed("json_html_parity")
    if data["shown"] != sum(1 for t in data["tasks"] if t["shown"]) or data["shown"] > TABLE_CAP:
        raise CheckFailed("json_html_parity")
    more = data["counts"]["tasks"] - data["shown"]
    m = re.search(r'data-more="(\d+)"', html_text)
    if (int(m.group(1)) if m else 0) != more:
        raise CheckFailed("json_html_parity")
    passed.append("json_html_parity")
    for t in data["tasks"]:
        if t["dead_reason"] is not None and t["dead_reason"] not in DEAD_PHRASES.values():
            raise CheckFailed("fixed_phrase_table")
    passed.append("fixed_phrase_table")
    return passed


def write_atomic(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(text)
    os.replace(tmp, path)


# ---- main -------------------------------------------------------------------------------
def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--flow-root", default=os.environ.get("FLOW_ROOT") or os.path.expanduser("~/.flow"))
    ap.add_argument("--out", default="site", help="directory that receives built-with-flow.html and data/this-week.json")
    ap.add_argument("--denylist", default=None, help="default: <flow-root>/playbooks/site-daily/denylist.txt")
    ap.add_argument("--queries", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "queries.sql"))
    ap.add_argument("--days", type=int, default=DEFAULT_DAYS)
    ap.add_argument("--now", default=None, help="ISO timestamp to treat as now (tests)")
    ap.add_argument("--no-gh", action="store_true", help="never call gh; keep only #refs whose line names the repo")
    ap.add_argument("--debug", action="store_true")
    args = ap.parse_args(argv)

    try:
        root = os.path.abspath(args.flow_root)
        denylist = Denylist.load(args.denylist or os.path.join(root, "playbooks", "site-daily", "denylist.txt"))
        now = parse_ts(args.now) if args.now else datetime.now(timezone.utc).astimezone()
        if now is None:
            raise CheckFailed("bad_now")
        con = open_ro(os.path.join(root, "flow.db"))
        try:
            queries = load_queries(args.queries)
            resolver = RefResolver(not args.no_gh, os.path.join(root, "playbooks", "site-daily", "gh-cache.json"))
            data = collect(root, con, queries, denylist, now, args.days, resolver)
        finally:
            con.close()
        resolver.save()
        html_text = render_html(data)
        json_text = json.dumps(data, indent=2, sort_keys=True) + "\n"
        passed = run_checks(html_text, json_text, data, denylist)
        write_atomic(os.path.join(args.out, "built-with-flow.html"), html_text)
        write_atomic(os.path.join(args.out, "data", "this-week.json"), json_text)
        print("ok: site-daily rendered %d tasks (%d in the table), leak checks passed: %s"
              % (data["counts"]["tasks"], data["shown"], ", ".join(passed)))
        return 0
    except CheckFailed as e:
        print("check failed: %s" % e.name, file=sys.stderr)
        return 2
    except Exception as e:  # never echo content; a class name is enough
        if args.debug:
            raise
        print("check failed: runtime_error (%s)" % type(e).__name__, file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
