"""Leak tests for site/daily/render.py.

Run: python3 -m unittest discover site/daily
Stdlib only. Builds a fixture flow.db in a temp dir with flow-project tasks,
decoys from another project, a private-tagged task, and notes/messages that
carry things which must never reach the page.
"""
import contextlib
import io
import json
import os
import re
import sqlite3
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import render  # noqa: E402

NOW = "2026-11-11T12:00:00+05:30"
FRESH = "2026-11-10T09:00:00+05:30"      # inside the 7-day window
STALE = "2026-10-01T09:00:00+05:30"      # outside the window

DDL = """
CREATE TABLE projects (slug TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
  priority TEXT NOT NULL DEFAULT 'medium', work_dir TEXT NOT NULL, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, archived_at TEXT, deleted_at TEXT, terminal_group TEXT);
CREATE TABLE tasks (slug TEXT PRIMARY KEY, name TEXT NOT NULL, project_slug TEXT, status TEXT NOT NULL DEFAULT 'backlog',
  kind TEXT NOT NULL DEFAULT 'regular', playbook_slug TEXT, parent_slug TEXT, forked_from_slug TEXT, fork_reason TEXT,
  priority TEXT NOT NULL DEFAULT 'medium', work_dir TEXT NOT NULL, waiting_on TEXT, due_date TEXT, assignee TEXT,
  permission_mode TEXT NOT NULL DEFAULT 'auto', model TEXT, status_changed_at TEXT,
  session_provider TEXT NOT NULL DEFAULT 'claude', harness TEXT, session_id TEXT, session_started TEXT,
  session_last_resumed TEXT, session_path TEXT, worktree_path TEXT, inbox_seen_at TEXT, created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL, archived_at TEXT, deleted_at TEXT, auto_run_status TEXT, auto_run_pid INTEGER,
  auto_run_started TEXT, auto_run_finished TEXT, auto_run_log TEXT);
CREATE TABLE task_tags (task_slug TEXT NOT NULL, tag TEXT NOT NULL, created_at TEXT NOT NULL, PRIMARY KEY (task_slug, tag));
CREATE TABLE bus_messages (id TEXT PRIMARY KEY, created_at TEXT NOT NULL, kind TEXT NOT NULL, from_assignee TEXT NOT NULL,
  from_task_slug TEXT, sender_session_id TEXT, to_assignee TEXT NOT NULL, to_task_slug TEXT, body TEXT NOT NULL,
  urgent INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'pending', attempts INTEGER NOT NULL DEFAULT 0,
  next_notify_at TEXT, delivered_at TEXT, acked_at TEXT, waited_s REAL, acked_by TEXT, reply_to TEXT,
  consumed_by_identity TEXT, consumed_by_session TEXT, seen_at TEXT, options TEXT, fyi INTEGER NOT NULL DEFAULT 0,
  ping INTEGER NOT NULL DEFAULT 0, notified_at TEXT, answered_at TEXT, peer TEXT, origin_ref TEXT, delivery TEXT,
  delivery_error TEXT, attachments TEXT);
"""

EMAIL = "nupur.example@acme-corp.test"
DECOY_CUSTOMER = "Decoy Corp"
DENYLIST = [
    "acme", "nupur", "nupur example", "decoy corp", "acme-customer",
    "/Users/anshulsao", "xox", "ghp_", "ghs_", "sk-", "madal.facets.cloud/a/",
]


class Fixture:
    """A fake ~/.flow with a flow.db and the on-disk task directories."""

    def __init__(self, root):
        self.root = root
        self.db = os.path.join(root, "flow.db")
        self.denylist = os.path.join(root, "playbooks", "site-daily", "denylist.txt")
        os.makedirs(os.path.dirname(self.denylist))
        os.makedirs(os.path.join(root, "tasks"))
        os.makedirs(os.path.join(root, "kb"))
        self.write_denylist(DENYLIST)
        self.con = sqlite3.connect(self.db)
        self.con.executescript(DDL)
        self.project("flow")
        self.project("acme-customer")

    def write_denylist(self, tokens):
        with open(self.denylist, "w") as f:
            f.write("# fixture denylist\n")
            for t in tokens:
                f.write(t + "\n")

    def project(self, slug):
        self.con.execute(
            "INSERT INTO projects (slug,name,work_dir,created_at,updated_at) VALUES (?,?,?,?,?)",
            (slug, slug, "/tmp/x", STALE, STALE))
        self.con.commit()

    def task(self, slug, project="flow", status="done", parent=None, auto=None,
             created=FRESH, kind="regular", tags=(), deleted=None):
        self.con.execute(
            "INSERT INTO tasks (slug,name,project_slug,status,kind,parent_slug,priority,work_dir,"
            " created_at,updated_at,auto_run_status,deleted_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)",
            (slug, slug, project, status, kind, parent, "medium", "/tmp/x", created, created, auto, deleted))
        for t in tags:
            self.con.execute("INSERT INTO task_tags VALUES (?,?,?)", (slug, t, created))
        self.con.commit()
        os.makedirs(os.path.join(self.root, "tasks", slug, "updates"), exist_ok=True)
        with open(os.path.join(self.root, "tasks", slug, "brief.md"), "w") as f:
            f.write("# brief for " + slug + "\nPrivate brief text zebrabrief.\n")

    def note(self, slug, name, body):
        path = os.path.join(self.root, "tasks", slug, "updates", name)
        with open(path, "w") as f:
            f.write(body)

    def auto_log(self, slug, body, name="2026-11-10-090000.log"):
        d = os.path.join(self.root, "tasks", slug, "auto-runs")
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, name), "w") as f:
            f.write(body)

    def message(self, mid, from_task, to_assignee, to_task, body, fyi=0, created=FRESH):
        self.con.execute(
            "INSERT INTO bus_messages (id,created_at,kind,from_assignee,from_task_slug,to_assignee,to_task_slug,"
            " body,fyi) VALUES (?,?,?,?,?,?,?,?,?)",
            (mid, created, "message", "user", from_task, to_assignee, to_task, body, fyi))
        self.con.commit()


def build_standard(fx):
    """The scenario the brief describes."""
    # flow-project tasks
    fx.task("launch-driver", status="in-progress", auto=None)
    fx.task("child-readme", parent="launch-driver", status="done", auto="completed")
    fx.task("child-dead", parent="launch-driver", status="in-progress", auto="dead")
    fx.auto_log("child-dead", "Not logged in · Please run /login\nauto run for \"child-dead\" failed: exit status 1\n")
    fx.task("child-pr", parent="launch-driver", status="done", auto="completed")
    fx.note("child-pr", "2026-11-10-opened-pr.md",
            "Opened PR #120 and fixed issue #59 in Facets-cloud/flow. Zebranote sentence.\n"
            "Contact " + EMAIL + " and see /Users/anshulsao/secret/path.md for the plan. Also #4242 elsewhere.\n")
    fx.note("child-pr", "2026-11-11-second-note.md",
            "Second note. Zebrasecond.\nstep #1 of the plan, then #2.\n"
            "merged PR #77 this morning.\n")
    # a task whose work lives in sibling repos: only explicitly named refs survive
    fx.note("child-readme", "2026-11-10-readme.md",
            "Opened PR #8 in Facets-cloud/flow-mesh and PR #9 on github.com/Other/repo.\n"
            "merged PR #66 this morning.\nTracked in Facets-cloud/flow#118.\n")
    with open(os.path.join(fx.root, "tasks", "child-pr", "enterprise-plan.md"), "w") as f:
        f.write("stray file whose name must not appear\n")
    os.makedirs(os.path.join(fx.root, "tasks", "child-pr", "scratch"))
    # a flow task tagged private
    fx.task("secret-thing", status="done", auto="completed", tags=("private",))
    # a flow task whose slug carries a denylist token
    fx.task("flow-acme-bridge", status="done", auto="completed")
    # a flow task outside the window
    fx.task("old-task", status="done", auto="completed", created=STALE)
    # a soft-deleted flow task
    fx.task("deleted-task", status="done", auto="completed", deleted=FRESH)
    # decoy from another project
    fx.task("acme-migration", project="acme-customer", status="done", auto="dead")
    fx.note("acme-migration", "2026-11-10-acme.md",
            "ACME and Nupur Example signed. PR #777 in Facets-cloud/flow. " + EMAIL + "\n")
    fx.auto_log("acme-migration", "Not logged in · Please run /login\n")
    # bus traffic
    fx.message("m1", "child-readme", "user", "launch-driver", "readme done, see #120 in Facets-cloud/flow")
    fx.message("m2", "child-pr", "user", None, "need a decision about " + DECOY_CUSTOMER + " and #4242")
    fx.message("m3", "child-pr", "user", None, "fyi progress", fyi=1)
    fx.message("m4", "acme-migration", "user", None, "decoy ask about ACME")
    fx.message("m5", "acme-migration", "user", "launch-driver", "decoy cross-project message #777 Facets-cloud/flow")
    fx.message("m6", "child-readme", "user", "launch-driver", "old message", created=STALE)
    fx.message("m7", "launch-driver", "user", "secret-thing", "to a private task")


def run_render(fx, out, extra=()):
    argv = ["--flow-root", fx.root, "--out", out, "--now", NOW, "--no-gh", *extra]
    err = io.StringIO()
    outio = io.StringIO()
    with contextlib.redirect_stderr(err), contextlib.redirect_stdout(outio):
        rc = render.main(argv)
    return rc, outio.getvalue(), err.getvalue()


def read(path):
    with open(path) as f:
        return f.read()


class RenderTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.fx = Fixture(os.path.join(self.tmp.name, "flow-root"))
        self.out = os.path.join(self.tmp.name, "site")
        os.makedirs(self.out)
        build_standard(self.fx)

    def tearDown(self):
        self.fx.con.close()
        self.tmp.cleanup()

    def render_ok(self):
        rc, out, err = run_render(self.fx, self.out)
        self.assertEqual(rc, 0, msg="stderr: " + err)
        html = read(os.path.join(self.out, "built-with-flow.html"))
        data = json.loads(read(os.path.join(self.out, "data", "this-week.json")))
        return html, data

    # --- positive content -------------------------------------------------

    def test_output_contains_flow_tasks_and_counts(self):
        html, data = self.render_ok()
        for slug in ("launch-driver", "child-readme", "child-dead", "child-pr"):
            self.assertIn(slug, html)
        self.assertEqual(data["counts"]["tasks"], 4)
        self.assertEqual(data["counts"]["children"], 3)
        self.assertEqual(data["counts"]["done"], 2)
        self.assertEqual(data["counts"]["dead"], 1)
        self.assertEqual(data["counts"]["messages_between_tasks"], 1)
        self.assertEqual(data["counts"]["asks_to_human"], 1)
        self.assertEqual(data["counts"]["notes"], 3)
        by_slug = {t["slug"]: t for t in data["tasks"]}
        self.assertEqual(by_slug["child-pr"]["notes"], 2)
        self.assertEqual(by_slug["child-pr"]["status"], "done")
        self.assertEqual(by_slug["child-pr"]["auto_run_status"], "completed")
        self.assertTrue(by_slug["child-pr"]["child"])
        self.assertFalse(by_slug["launch-driver"]["child"])

    def test_pr_numbers_from_flow_tasks_appear_when_repo_named(self):
        html, data = self.render_ok()
        by_slug = {t["slug"]: t for t in data["tasks"]}
        self.assertEqual(sorted(by_slug["child-pr"]["refs"]), [59, 120])
        self.assertIn("#120", html)
        self.assertIn("#59", html)
        self.assertIn("github.com/Facets-cloud/flow/issues/120", html)

    def test_dead_run_gets_templated_phrase_not_log_text(self):
        html, data = self.render_ok()
        self.assertIn("waited on a login", html)
        self.assertNotIn("Please run", html)
        self.assertNotIn("/login", html)
        by_slug = {t["slug"]: t for t in data["tasks"]}
        self.assertEqual(by_slug["child-dead"]["dead_reason"], "waited on a login")
        self.assertIn(by_slug["child-dead"]["dead_reason"], render.DEAD_PHRASES.values())

    def test_leaves_behind_block_lists_top_level_names_only(self):
        html, data = self.render_ok()
        names = {e["name"] for e in data["leaves_behind"]}
        self.assertIn("brief.md", names)
        self.assertIn("updates/", names)
        self.assertIn("auto-runs/", names)
        self.assertNotIn("2026-11-10-opened-pr.md", html)
        self.assertNotIn("opened-pr", html)
        for e in data["leaves_behind"]:
            self.assertFalse(e["name"].startswith("/"))
            self.assertNotIn("..", e["name"])

    # --- leak checks ------------------------------------------------------

    def test_output_excludes_private_and_decoys(self):
        html, data = self.render_ok()
        blob = html + json.dumps(data)
        for needle in ("acme", "ACME", "Acme", "Nupur", "nupur", "secret-thing", EMAIL,
                       "/Users/", "anshulsao", DECOY_CUSTOMER, "Decoy", "old-task", "deleted-task",
                       "zebrabrief", "Zebranote", "Zebrasecond", "readme done", "decision about",
                       "acme-customer"):
            self.assertNotIn(needle, blob, msg="leaked: " + needle)

    def test_decoy_project_pr_numbers_never_appear(self):
        html, data = self.render_ok()
        blob = html + json.dumps(data)
        self.assertNotIn("777", blob)

    def test_unattributed_number_without_repo_or_gh_is_dropped(self):
        html, data = self.render_ok()
        blob = html + json.dumps(data)
        self.assertNotIn("4242", blob)

    def test_resolved_refs_need_a_pr_cue_and_no_other_repo(self):
        # simulate gh saying "every number exists in Facets-cloud/flow"
        orig = render.RefResolver.resolves
        render.RefResolver.resolves = lambda self, n: True
        try:
            rc, out, err = run_render(self.fx, self.out, extra=())
            # --no-gh is in run_render; re-run with gh "enabled"
            argv = ["--flow-root", self.fx.root, "--out", self.out, "--now", NOW]
            with contextlib.redirect_stderr(io.StringIO()), contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(render.main(argv), 0)
        finally:
            render.RefResolver.resolves = orig
        data = json.loads(read(os.path.join(self.out, "data", "this-week.json")))
        by_slug = {t["slug"]: t for t in data["tasks"]}
        refs = by_slug["child-pr"]["refs"]
        self.assertIn(77, refs)            # "merged PR #77" -> cue word, resolves
        self.assertIn(120, refs)
        self.assertNotIn(1, refs)          # "step #1" has no PR/issue cue
        self.assertNotIn(2, refs)
        self.assertNotIn(4242, refs)       # "Also #4242 elsewhere" has no cue
        readme = by_slug["child-readme"]["refs"]
        self.assertEqual(readme, [118, 120])  # named explicitly (note + message) -> kept
        self.assertNotIn(8, readme)        # names a sibling repo
        self.assertNotIn(9, readme)        # names another github repo
        self.assertNotIn(66, readme)       # cued and "resolves", but the task talks about other repos

    def test_leaves_behind_aggregates_stray_files(self):
        html, data = self.render_ok()
        names = {e["name"] for e in data["leaves_behind"]}
        self.assertIn("scratch/", names)
        self.assertIn("other files", names)
        self.assertNotIn("enterprise-plan.md", html)
        self.assertNotIn("enterprise", html)

    def test_denylist_token_in_slug_removes_task(self):
        html, data = self.render_ok()
        self.assertNotIn("flow-acme-bridge", html)
        self.assertEqual(data["counts"]["tasks"], 4)
        # and a token planted after the fact removes a task that was fine before
        self.fx.write_denylist(DENYLIST + ["child-readme"])
        html, data = self.render_ok()
        self.assertNotIn("child-readme", html)
        self.assertEqual(data["counts"]["tasks"], 3)
        self.assertEqual(data["counts"]["messages_between_tasks"], 0)

    def test_json_and_html_agree_on_counts(self):
        html, data = self.render_ok()
        c = data["counts"]
        for key in ("tasks", "children", "done", "dead", "messages_between_tasks", "asks_to_human", "notes"):
            m = re.search(r'data-count="%s">(\d+)<' % key, html)
            self.assertIsNotNone(m, msg="no counter for " + key)
            self.assertEqual(int(m.group(1)), c[key], msg=key)
        self.assertEqual(len(re.findall(r'class="bwf-slug"', html)), len(data["tasks"]))

    def test_missing_denylist_exits_nonzero_with_no_output(self):
        os.remove(self.fx.denylist)
        rc, out, err = run_render(self.fx, self.out)
        self.assertNotEqual(rc, 0)
        self.assertIn("denylist_present", err)
        self.assertFalse(os.path.exists(os.path.join(self.out, "built-with-flow.html")))
        self.assertFalse(os.path.exists(os.path.join(self.out, "data", "this-week.json")))

    def test_empty_denylist_is_refused(self):
        self.fx.write_denylist([])
        rc, out, err = run_render(self.fx, self.out)
        self.assertNotEqual(rc, 0)
        self.assertIn("denylist_present", err)

    def test_fail_closed_names_check_but_never_the_token(self):
        # "completed" is legitimately in the output; planting it as a token must
        # stop the run, write nothing, and report only the check name.
        self.fx.write_denylist(DENYLIST + ["completed"])
        rc, out, err = run_render(self.fx, self.out)
        self.assertNotEqual(rc, 0)
        self.assertIn("denylist_scan", err)
        self.assertNotIn("completed", err)
        self.assertNotIn("completed", out)
        self.assertFalse(os.path.exists(os.path.join(self.out, "built-with-flow.html")))
        self.assertFalse(os.path.exists(os.path.join(self.out, "data", "this-week.json")))

    def test_previous_output_survives_a_failed_run(self):
        self.render_ok()
        before = read(os.path.join(self.out, "built-with-flow.html"))
        self.fx.write_denylist(DENYLIST + ["completed"])
        rc, out, err = run_render(self.fx, self.out)
        self.assertNotEqual(rc, 0)
        self.assertEqual(before, read(os.path.join(self.out, "built-with-flow.html")))

    def test_scope_constant_is_flow_only(self):
        self.assertEqual(render.PROJECT_SLUGS, ("flow",))

    def test_cli_entrypoint(self):
        rc = subprocess.run(
            [sys.executable, os.path.join(HERE, "render.py"), "--flow-root", self.fx.root,
             "--out", self.out, "--now", NOW, "--no-gh"],
            capture_output=True, text=True)
        self.assertEqual(rc.returncode, 0, msg=rc.stderr)
        self.assertIn("leak checks passed", rc.stdout)


class DenylistMatchTest(unittest.TestCase):
    def test_match_is_case_insensitive_and_bounded_at_start(self):
        dl = render.Denylist(["Acme", "sk-", "xox"])
        self.assertTrue(dl.hit("flow-ACME-bridge"))
        self.assertTrue(dl.hit("key sk-abc"))
        self.assertTrue(dl.hit("xoxb-1"))
        # secret prefixes must not fire on ordinary words that merely contain them
        self.assertFalse(dl.hit("task-list"))
        self.assertFalse(dl.hit("ask-mac"))
        self.assertFalse(dl.hit("flow-readme"))


@unittest.skipUnless(
    os.path.exists(os.path.join(os.environ.get("FLOW_ROOT", os.path.expanduser("~/.flow")), "flow.db")),
    "no real ~/.flow on this machine (CI)")
class RealRootTest(unittest.TestCase):
    """Runs the renderer read-only against the real flow root and asserts the
    output passes the denylist and contains no absolute paths."""

    def test_real_root_output_is_clean(self):
        root = os.environ.get("FLOW_ROOT", os.path.expanduser("~/.flow"))
        with tempfile.TemporaryDirectory() as out:
            err = io.StringIO()
            with contextlib.redirect_stderr(err), contextlib.redirect_stdout(io.StringIO()):
                rc = render.main(["--flow-root", root, "--out", out, "--no-gh"])
            self.assertEqual(rc, 0, msg=err.getvalue())
            html = read(os.path.join(out, "built-with-flow.html"))
            data = read(os.path.join(out, "data", "this-week.json"))
            blob = html + data
            dl = render.Denylist.load(os.path.join(root, "playbooks", "site-daily", "denylist.txt"))
            self.assertFalse(dl.hit(blob))
            self.assertIsNone(render.ABS_PATH_RE.search(blob))
            self.assertIsNone(render.EMAIL_RE.search(blob))
            self.assertNotIn(os.path.expanduser("~"), blob)


if __name__ == "__main__":
    unittest.main()
