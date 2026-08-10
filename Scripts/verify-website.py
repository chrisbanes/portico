#!/usr/bin/env python3
"""Verify the dependency-free Portico marketing site."""

from __future__ import annotations

import re
import struct
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlparse


ROOT = Path(__file__).resolve().parents[1]
WEBSITE = ROOT / "website"
INDEX = WEBSITE / "index.html"
PAGES_WORKFLOW = ROOT / ".github/workflows/pages.yml"
CANONICAL_PAGES_LINES = """\
name: Pages
on:
  push:
    branches: [main]
    paths:
      - website/**
      - Scripts/verify-website.py
      - .github/workflows/pages.yml
  pull_request:
    paths:
      - website/**
      - Scripts/verify-website.py
      - .github/workflows/pages.yml
  workflow_dispatch:
concurrency:
  group: pages
  cancel-in-progress: false
permissions:
  contents: read
jobs:
  build:
    name: Validate Pages artifact
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803
      - name: Verify static site
        run: python3 Scripts/verify-website.py
      - name: Upload Pages artifact
        uses: actions/upload-pages-artifact@7b1f4a764d45c48632c6b24a0339c27f5614fb0b
        with:
          path: website
  deploy:
    name: Deploy Pages
    if: github.event_name != 'pull_request' && github.ref == 'refs/heads/main'
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    runs-on: ubuntu-latest
    needs: build
    permissions:
      contents: read
      pages: write
      id-token: write
    steps:
      - name: Configure Pages
        uses: actions/configure-pages@983d7736d9b0ae728b81ab479565c72886d7745b
      - name: Deploy
        id: deployment
        uses: actions/deploy-pages@d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e
""".splitlines()


class Page(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.tags: list[tuple[str, dict[str, str]]] = []
        self.text: list[str] = []
        self._in_h1 = False
        self.h1: list[str] = []
        self._in_title = False
        self.title: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attributes = {name: value or "" for name, value in attrs}
        self.tags.append((tag, attributes))
        if tag == "h1":
            self._in_h1 = True
        if tag == "title":
            self._in_title = True

    def handle_endtag(self, tag: str) -> None:
        if tag == "h1":
            self._in_h1 = False
        if tag == "title":
            self._in_title = False

    def handle_data(self, data: str) -> None:
        self.text.append(data)
        if self._in_h1:
            self.h1.append(data)
        if self._in_title:
            self.title.append(data)


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def page() -> Page:
    require(INDEX.is_file(), "missing website/index.html")
    document = Page()
    document.feed(INDEX.read_text(encoding="utf-8"))
    return document


def has_tag(document: Page, tag: str, **attributes: str) -> bool:
    return any(
        current_tag == tag and all(values.get(name) == value for name, value in attributes.items())
        for current_tag, values in document.tags
    )


def document_text(document: Page) -> str:
    return " ".join(document.text)


def check_local(value: str, kind: str, *, allow_external: bool = False) -> None:
    parsed = urlparse(value)
    if parsed.scheme or parsed.netloc or value.startswith("//"):
        require(allow_external, f"remote {kind} asset: {value}")
        return
    if not parsed.path:
        return
    require(not parsed.path.startswith("/"), f"root-relative {kind} asset: {value}")
    require(".." not in Path(parsed.path).parts, f"parent-relative {kind} asset: {value}")
    resolved = (WEBSITE / parsed.path).resolve()
    require(resolved.is_relative_to(WEBSITE.resolve()), f"unsafe {kind} asset: {value}")
    require(resolved.is_file(), f"missing local {kind}: {value}")


def require_rejected_local(value: str, kind: str, expected_error: str) -> None:
    try:
        check_local(value, kind)
    except AssertionError as error:
        require(str(error) == expected_error, f"expected {expected_error}, got {error}")
    else:
        raise AssertionError(f"accepted invalid local {kind}: {value}")


def structure() -> None:
    document = page()
    text = document_text(document)
    for identifier in ("main-content", "how-it-works", "app-preview", "features", "architecture", "install"):
        require(any(values.get("id") == identifier for _, values in document.tags), f"missing #{identifier}")
    for landmark in ("header", "nav", "main", "footer"):
        require(any(tag == landmark for tag, _ in document.tags), f"missing {landmark} landmark")
    require(has_tag(document, "html", lang="en"), "html lang must be en")
    require(has_tag(document, "a", href="#main-content"), "missing skip link")
    require(has_tag(document, "button", **{"aria-controls": "site-nav"}), "missing mobile navigation control")
    require(any(values.get("aria-expanded") in {"true", "false"} for tag, values in document.tags if tag == "button"), "mobile navigation needs aria-expanded")
    require("Portico is a native macOS menu-bar app" in text, "missing approved hero copy")
    require("Install with Homebrew." in text, "missing Homebrew installation heading")
    require("brew install --cask chrisbanes/tap/portico" in text, "missing Homebrew installation command")
    require("selected working name" in text and "legally cleared" in text, "missing working-name disclaimer")
    require(any(values.get("href") == "https://github.com/chrisbanes/portico" for tag, values in document.tags if tag == "a"), "missing GitHub URL")
    require(len(document.h1) == 1 and "".join(document.h1).strip() == "Give your Mac apps a private URL.", "expected exactly one approved H1")
    require(any(tag == "a" and values.get("href") == "#how-it-works" and values.get("class") == "button" for tag, values in document.tags), "missing workflow CTA")
    require("first-portal" in text and "foo-app" in text, "missing canonical Portal identity example")
    require("https://foo-app.<tailnet>.ts.net" in text, "missing Assigned Name Portal URL example")
    require("Assigned Name may differ" in text and "URL follows Assigned Name" in text, "missing Portal Name and Assigned Name explanation")
    require(not any("data-reveal" in values for _, values in document.tags), "reveal attributes must not control content visibility")
    require(not any("app-window" in values.get("class", "").split() for _, values in document.tags), "simulated app window must be removed")
    feature_articles = [values for tag, values in document.tags if tag == "article" and "feature-card" in values.get("class", "").split()]
    require(len(feature_articles) == 3, "expected exactly three feature articles")
    expected_screenshots = {
        "assets/portal-detail.png": (1507, 1044),
        "assets/add-portal.png": (1507, 1044),
        "assets/menu-bar.png": (1325, 1187),
    }
    for source, (width, height) in expected_screenshots.items():
        matches = [values for tag, values in document.tags if tag == "img" and values.get("src") == source]
        require(len(matches) == 1, f"expected one local screenshot: {source}")
        screenshot = matches[0]
        require(screenshot.get("alt"), f"screenshot needs useful alt text: {source}")
        require(screenshot.get("width") == str(width) and screenshot.get("height") == str(height), f"screenshot dimensions must be explicit: {source}")
    anchors = {values.get("href") for tag, values in document.tags if tag == "a"}
    for anchor in ("#how-it-works", "#features", "#install"):
        require(anchor in anchors, f"missing navigation anchor {anchor}")
    stylesheets = [values.get("href") for tag, values in document.tags if tag == "link" and values.get("rel") == "stylesheet"]
    require(stylesheets == ["styles.css"], "stylesheets must be local styles.css only")
    scripts = [values.get("src") for tag, values in document.tags if tag == "script" and values.get("src")]
    require(scripts == ["script.js"], "scripts must be local script.js only")
    require(has_tag(document, "link", rel="icon", href="assets/portico-mark.svg", type="image/svg+xml"), "missing Portico site icon")
    require(has_tag(document, "img", src="assets/portico-mark.svg", alt=""), "wordmark must use the Portico mark")
    for filename in ("styles.css", "script.js", ".nojekyll"):
        require((WEBSITE / filename).is_file(), f"missing website/{filename}")
    for tag, values in document.tags:
        if tag == "a" and values.get("href"):
            check_local(values["href"], "link", allow_external=True)
        if tag == "script" and values.get("src"):
            check_local(values["src"], "script")
        if tag == "link" and {"stylesheet", "icon", "preload"}.intersection(values.get("rel", "").split()) and values.get("href"):
            check_local(values["href"], values.get("rel", "link"))
        if tag in {"img", "source", "video", "audio"} and values.get("src"):
            check_local(values["src"], tag)
        if tag in {"img", "source"} and values.get("srcset"):
            for candidate in values["srcset"].split(","):
                check_local(candidate.strip().split()[0], tag)
    require_rejected_local("missing-file.html", "link", "missing local link: missing-file.html")


def styles() -> None:
    stylesheet_path = WEBSITE / "styles.css"
    require(stylesheet_path.is_file(), "missing website/styles.css")
    stylesheet = stylesheet_path.read_text(encoding="utf-8")
    for expected in ("--navy", "--blue", "--sky", "--paper", "--ink", "--sans", ".skip-link", ":focus-visible", ".workflow-list", ".screen-frame", ".architecture-points", "max-width: 760", "min-height: 44px", "prefers-color-scheme: dark", "prefers-reduced-motion", "overflow-x: hidden"):
        require(expected in stylesheet, f"missing style {expected}")
    for obsolete in ("--limestone", "--oxidized", ".hero-portal", ".app-window", ".feature-grid"):
        require(obsolete not in stylesheet, f"obsolete style remains: {obsolete}")


def behavior() -> None:
    script_path = WEBSITE / "script.js"
    require(script_path.is_file(), "missing website/script.js")
    script = script_path.read_text(encoding="utf-8")
    for expected in ("document.documentElement.classList.add", "aria-expanded", "Escape", "matchMedia"):
        require(expected in script, f"missing behavior {expected}")
    for obsolete in ("IntersectionObserver", "data-reveal", "prefers-reduced-motion"):
        require(obsolete not in script, f"obsolete behavior remains: {obsolete}")


def metadata() -> None:
    document = page()
    require("".join(document.title).strip() == "Portico - private URLs for Mac apps", "missing document title")
    expected = {
        ("name", "description"): "Portico gives services reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.",
        ("property", "og:title"): "Portico - private URLs for Mac apps",
        ("property", "og:description"): "Portico gives services reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.",
        ("property", "og:type"): "website",
        ("property", "og:url"): "https://chrisbanes.github.io/portico/",
        ("property", "og:image"): "https://chrisbanes.github.io/portico/assets/og.png",
        ("name", "twitter:card"): "summary_large_image",
        ("name", "theme-color"): "#f4f7fb",
    }
    for (attribute, name), content in expected.items():
        require(any(tag == "meta" and values.get(attribute) == name and values.get("content") == content for tag, values in document.tags), f"missing metadata {name}")
    image = WEBSITE / "assets/og.png"
    require(image.is_file(), "missing website/assets/og.png")
    with image.open("rb") as file:
        header = file.read(24)
    require(header.startswith(b"\x89PNG\r\n\x1a\n"), "og.png is not a PNG")
    require(len(header) == 24, "og.png is truncated")
    width, height = struct.unpack(">II", header[16:24])
    require((width, height) == (1200, 630), "og.png must be 1200x630")


def active_yaml_lines(workflow: str) -> list[str]:
    return [line.split("#", 1)[0].rstrip() for line in workflow.splitlines() if line.split("#", 1)[0].strip()]


def yaml_block(lines: list[str], start: str) -> list[str]:
    try:
        position = lines.index(start)
    except ValueError as error:
        raise AssertionError(f"missing YAML block: {start}") from error
    indentation = len(start) - len(start.lstrip())
    block: list[str] = []
    for line in lines[position + 1 :]:
        if len(line) - len(line.lstrip()) <= indentation:
            break
        block.append(line)
    return block


def has_yaml_sequence(lines: list[str], expected: list[str]) -> bool:
    return any(lines[position : position + len(expected)] == expected for position in range(len(lines)))


def validate_pages_workflow(workflow: str) -> None:
    lines = active_yaml_lines(workflow)
    for line in lines:
        match = re.fullmatch(r"\s*-\s+uses:\s*(\S+)", line)
        if match:
            require(re.fullmatch(r"[^@\s]+@[0-9a-f]{40}", match.group(1)) is not None, "Pages workflow has unpinned action")
    top_level_permissions = yaml_block(lines, "permissions:")
    require(top_level_permissions == ["  contents: read"], "Pages workflow must not grant write permissions at top level")
    build = yaml_block(lines, "  build:")
    require("    permissions:" not in build, "Pages build job must inherit read-only permissions")
    require(not any("actions/configure-pages@" in line for line in build), "Pages build job must not configure Pages")
    deploy = yaml_block(lines, "  deploy:")
    require(
        has_yaml_sequence(deploy, ["    permissions:", "      contents: read", "      pages: write", "      id-token: write", "    steps:"]),
        "Pages deploy job must own Pages write permissions",
    )
    require(
        has_yaml_sequence(
            deploy,
            [
                "      - name: Configure Pages",
                "        uses: actions/configure-pages@983d7736d9b0ae728b81ab479565c72886d7745b",
                "      - name: Deploy",
                "        id: deployment",
                "        uses: actions/deploy-pages@d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e",
            ],
        ),
        "Pages deploy job must configure Pages immediately before deployment",
    )
    require(lines == CANONICAL_PAGES_LINES, "Pages workflow must match the canonical active YAML")


def require_rejected_pages_workflow(workflow: str, expected_error: str) -> None:
    try:
        validate_pages_workflow(workflow)
    except AssertionError as error:
        require(str(error) == expected_error, f"expected {expected_error}, got {error}")
    else:
        raise AssertionError(f"Pages workflow accepted invalid permissions: {expected_error}")


def pages() -> None:
    require(PAGES_WORKFLOW.is_file(), "missing Pages workflow")
    workflow = PAGES_WORKFLOW.read_text(encoding="utf-8")
    validate_pages_workflow(workflow)
    require_rejected_pages_workflow(
        workflow.replace("permissions:\n  contents: read", "permissions:\n  contents: read\n  pages: write", 1),
        "Pages workflow must not grant write permissions at top level",
    )
    require_rejected_pages_workflow(
        workflow.replace("    name: Validate Pages artifact", "    permissions:\n      pages: write\n    name: Validate Pages artifact", 1),
        "Pages build job must inherit read-only permissions",
    )
    configure_pages_step = "      - name: Configure Pages\n        uses: actions/configure-pages@983d7736d9b0ae728b81ab479565c72886d7745b\n"
    require_rejected_pages_workflow(
        workflow.replace(configure_pages_step, "", 1),
        "Pages deploy job must configure Pages immediately before deployment",
    )
    require_rejected_pages_workflow(
        workflow.replace("      - name: Upload Pages artifact", configure_pages_step + "      - name: Upload Pages artifact", 1),
        "Pages build job must not configure Pages",
    )


CHECKS = {"structure": structure, "styles": styles, "behavior": behavior, "metadata": metadata, "pages": pages}


def main(arguments: list[str]) -> int:
    requested = arguments or ["all"]
    unknown = [mode for mode in requested if mode != "all" and mode not in CHECKS]
    if unknown:
        print(f"unknown mode(s): {', '.join(unknown)}; expected all or: {', '.join(CHECKS)}", file=sys.stderr)
        return 2
    if "all" in requested:
        modes = CHECKS.items()
    else:
        modes = [(mode, CHECKS[mode]) for mode in requested]
    try:
        for name, check in modes:
            check()
            print(f"PASS {name}")
    except AssertionError as error:
        print(f"FAIL {error}")
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
