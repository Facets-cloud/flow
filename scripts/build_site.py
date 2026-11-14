#!/usr/bin/env python3
"""Assemble the GitHub Pages publish tree for the flow homepage.

    python3 scripts/build_site.py out            # from the repo root
    make site                                    # same thing

What it does, in order:
  1. copies site/ to <out>/ (styles, assets, script.js, index.html, ...)
  2. replaces the site/demo -> ../docs/demo symlink with the real files,
     so Pages serves demo/<file> for every <video src="demo/...">
  3. splices site/built-with-flow.html (written daily by the site-daily
     playbook) into index.html in place of the block delimited by
         <!-- built-with-flow -->  ...  <!-- /built-with-flow -->
     The block in the source index.html holds a one-line fallback that
     stays visible if the fragment is missing or this step never ran, so
     the raw page still reads. index.html in site/ is never modified.
  4. touches <out>/.nojekyll

Stdlib only. .github/workflows/pages.yml runs exactly this script.
"""
import argparse
import shutil
import sys
from pathlib import Path

MARKER_START = "<!-- built-with-flow -->"
MARKER_END = "<!-- /built-with-flow -->"


class MarkerError(ValueError):
    """index.html does not carry exactly one well-formed marker block."""


def assemble_index(index_html: str, fragment_html):
    """Return index_html with the marker block replaced by fragment_html.

    fragment_html=None (no fragment available) returns index_html unchanged,
    fallback included. A missing or unbalanced marker pair is an error: the
    page must always carry the slot so the daily fragment has a home.
    """
    start = index_html.find(MARKER_START)
    if start < 0:
        raise MarkerError(f"index.html has no {MARKER_START} marker")
    end = index_html.find(MARKER_END, start + len(MARKER_START))
    if end < 0:
        raise MarkerError(f"index.html has {MARKER_START} but no {MARKER_END}")
    if index_html.find(MARKER_START, start + len(MARKER_START)) >= 0:
        raise MarkerError(f"index.html has more than one {MARKER_START} marker")
    if fragment_html is None:
        return index_html
    end += len(MARKER_END)
    return index_html[:start] + fragment_html.strip("\n") + index_html[end:]


def build(repo: Path, out: Path) -> Path:
    repo = Path(repo)
    out = Path(out)
    site = repo / "site"
    if not (site / "index.html").is_file():
        raise FileNotFoundError(f"{site / 'index.html'} not found")

    if out.exists():
        shutil.rmtree(out)
    # symlinks=True keeps site/demo as a link for a moment; we replace it below.
    shutil.copytree(site, out, symlinks=True)

    demo_out = out / "demo"
    if demo_out.is_symlink() or demo_out.exists():
        if demo_out.is_symlink() or demo_out.is_file():
            demo_out.unlink()
        else:
            shutil.rmtree(demo_out)
    demo_src = repo / "docs" / "demo"
    if demo_src.is_dir():
        shutil.copytree(demo_src, demo_out)
    else:
        demo_out.mkdir()

    fragment_path = site / "built-with-flow.html"
    fragment = fragment_path.read_text(encoding="utf-8") if fragment_path.is_file() else None
    index_path = out / "index.html"
    index_path.write_text(
        assemble_index((site / "index.html").read_text(encoding="utf-8"), fragment),
        encoding="utf-8",
    )

    (out / ".nojekyll").touch()
    return out


def main(argv=None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("out", nargs="?", default="out", help="output directory (default: out)")
    p.add_argument("--repo", default=None, help="repo root (default: parent of scripts/)")
    a = p.parse_args(argv)
    repo = Path(a.repo) if a.repo else Path(__file__).resolve().parent.parent
    try:
        out = build(repo, Path(a.out))
    except (MarkerError, FileNotFoundError) as e:
        print(f"build_site: {e}", file=sys.stderr)
        return 1
    fragment = "spliced" if (repo / "site" / "built-with-flow.html").is_file() else "MISSING (fallback kept)"
    print(f"site assembled -> {out}/  (built-with-flow fragment: {fragment})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
