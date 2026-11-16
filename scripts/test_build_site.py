"""Tests for scripts/build_site.py — the GitHub Pages assembler.

Run: python3 -m unittest discover -s scripts
"""
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import build_site  # noqa: E402

FALLBACK = "This section is generated daily"


def make_tree(root: Path, *, with_fragment=True, with_marker=True):
    site = root / "site"
    (site / "assets").mkdir(parents=True)
    (root / "docs" / "demo").mkdir(parents=True)
    (root / "docs" / "demo" / "clip.mp4").write_bytes(b"\x00video")
    (site / "assets" / "logo.svg").write_text("<svg/>")
    (site / "styles.css").write_text("body{}")
    os.symlink("../docs/demo", site / "demo")
    body = ["<!doctype html><html><body>", "<p>before</p>"]
    if with_marker:
        body += [
            build_site.MARKER_START,
            '<section class="bwf-fallback"><p>' + FALLBACK + "</p></section>",
            build_site.MARKER_END,
        ]
    body += ["<p>after</p>", "</body></html>"]
    (site / "index.html").write_text("\n".join(body))
    if with_fragment:
        (site / "built-with-flow.html").write_text(
            '<style>.bwf{}</style>\n<section id="built-with-flow" class="bwf">FRAGMENT</section>\n'
        )
    return site


class AssembleIndexTest(unittest.TestCase):
    def test_marker_block_is_replaced_by_fragment(self):
        html = "a\n" + build_site.MARKER_START + "\nFALLBACK\n" + build_site.MARKER_END + "\nb"
        out = build_site.assemble_index(html, "<section>FRAG</section>")
        self.assertIn("<section>FRAG</section>", out)
        self.assertNotIn("FALLBACK", out)
        self.assertNotIn(build_site.MARKER_START, out)
        self.assertNotIn(build_site.MARKER_END, out)
        self.assertTrue(out.startswith("a\n"))
        self.assertTrue(out.endswith("\nb"))

    def test_missing_fragment_keeps_fallback(self):
        html = "a\n" + build_site.MARKER_START + "\nFALLBACK\n" + build_site.MARKER_END + "\nb"
        out = build_site.assemble_index(html, None)
        self.assertEqual(out, html)

    def test_missing_marker_raises(self):
        with self.assertRaises(build_site.MarkerError):
            build_site.assemble_index("<html>no marker</html>", "<section>FRAG</section>")

    def test_unbalanced_marker_raises(self):
        with self.assertRaises(build_site.MarkerError):
            build_site.assemble_index(build_site.MARKER_START + " only", "<section>FRAG</section>")


class BuildTreeTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)

    def test_build_copies_site_resolves_demo_and_splices_fragment(self):
        site = make_tree(self.tmp)
        out = self.tmp / "out"
        build_site.build(self.tmp, out)
        index = (out / "index.html").read_text()
        self.assertIn("FRAGMENT", index)
        self.assertNotIn(FALLBACK, index)
        self.assertNotIn(build_site.MARKER_START, index)
        self.assertTrue((out / "styles.css").is_file())
        self.assertTrue((out / "assets" / "logo.svg").is_file())
        # demo/ is a real directory with the docs/demo files, not a symlink
        self.assertFalse((out / "demo").is_symlink())
        self.assertTrue((out / "demo" / "clip.mp4").is_file())
        self.assertTrue((out / ".nojekyll").is_file())
        # the raw fragment is not needed on Pages but keeping it is harmless;
        # the source index.html must still carry the fallback (untouched)
        self.assertIn(FALLBACK, (site / "index.html").read_text())

    def test_build_without_fragment_keeps_fallback(self):
        make_tree(self.tmp, with_fragment=False)
        out = self.tmp / "out"
        build_site.build(self.tmp, out)
        index = (out / "index.html").read_text()
        self.assertIn(FALLBACK, index)
        self.assertNotIn("FRAGMENT", index)

    def test_build_is_idempotent_over_existing_out(self):
        make_tree(self.tmp)
        out = self.tmp / "out"
        out.mkdir()
        (out / "stale.txt").write_text("old")
        build_site.build(self.tmp, out)
        self.assertFalse((out / "stale.txt").exists())

    def test_cli(self):
        make_tree(self.tmp)
        out = self.tmp / "out"
        r = subprocess.run(
            [sys.executable, str(HERE / "build_site.py"), "--repo", str(self.tmp), str(out)],
            capture_output=True, text=True,
        )
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("FRAGMENT", (out / "index.html").read_text())


if __name__ == "__main__":
    unittest.main()
