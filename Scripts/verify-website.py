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


class Page(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.tags: list[tuple[str, dict[str, str]]] = []
        self.text: list[str] = []
        self._in_h1 = False
        self.h1: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attributes = {name: value or "" for name, value in attrs}
        self.tags.append((tag, attributes))
        if tag == "h1":
            self._in_h1 = True

    def handle_endtag(self, tag: str) -> None:
        if tag == "h1":
            self._in_h1 = False

    def handle_data(self, data: str) -> None:
        self.text.append(data)
        if self._in_h1:
            self.h1.append(data)


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


def check_local(value: str, kind: str) -> None:
    parsed = urlparse(value)
    require(not parsed.scheme and not parsed.netloc and not value.startswith("//"), f"remote {kind} asset: {value}")
    require(not parsed.path.startswith("/"), f"root-relative {kind} asset: {value}")
    require(".." not in Path(parsed.path).parts, f"parent-relative {kind} asset: {value}")
    resolved = (WEBSITE / parsed.path).resolve()
    require(resolved.is_relative_to(WEBSITE.resolve()), f"unsafe {kind} asset: {value}")


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
    require("Preview instructions" in text, "missing Preview instructions")
    require("selected working name" in text and "legally cleared" in text, "missing working-name disclaimer")
    require(any(values.get("href") == "https://github.com/chrisbanes/portico" for tag, values in document.tags if tag == "a"), "missing GitHub URL")
    require(len(document.h1) == 1 and "".join(document.h1).strip() == "Your local apps, through a private door.", "expected exactly one approved H1")
    anchors = {values.get("href") for tag, values in document.tags if tag == "a"}
    for anchor in ("#how-it-works", "#features", "#install"):
        require(anchor in anchors, f"missing navigation anchor {anchor}")
    stylesheets = [values.get("href") for tag, values in document.tags if tag == "link" and values.get("rel") == "stylesheet"]
    require(stylesheets == ["styles.css"], "stylesheets must be local styles.css only")
    scripts = [values.get("src") for tag, values in document.tags if tag == "script" and values.get("src")]
    require(scripts == ["script.js"], "scripts must be local script.js only")
    for filename in ("styles.css", "script.js", ".nojekyll"):
        require((WEBSITE / filename).is_file(), f"missing website/{filename}")
    for tag, values in document.tags:
        if tag == "script" and values.get("src"):
            check_local(values["src"], "script")
        if tag == "link" and {"stylesheet", "icon", "preload"}.intersection(values.get("rel", "").split()) and values.get("href"):
            check_local(values["href"], values.get("rel", "link"))
        if tag in {"img", "source", "video", "audio"} and values.get("src"):
            check_local(values["src"], tag)
        if tag in {"img", "source"} and values.get("srcset"):
            for candidate in values["srcset"].split(","):
                check_local(candidate.strip().split()[0], tag)


def styles() -> None:
    stylesheet_path = WEBSITE / "styles.css"
    require(stylesheet_path.is_file(), "missing website/styles.css")
    stylesheet = stylesheet_path.read_text(encoding="utf-8")
    for expected in ("--limestone", "--ink", "--oxidized", "--coral", ".skip-link", ":focus-visible", ".hero-portal", ".route-flow", ".app-window", ".feature-grid", "max-width: 760", "prefers-reduced-motion", "overflow-x: hidden"):
        require(expected in stylesheet, f"missing style {expected}")


def behavior() -> None:
    script_path = WEBSITE / "script.js"
    require(script_path.is_file(), "missing website/script.js")
    script = script_path.read_text(encoding="utf-8")
    for expected in ("document.documentElement.classList.add", "aria-expanded", "Escape", "IntersectionObserver", "prefers-reduced-motion"):
        require(expected in script, f"missing behavior {expected}")


def metadata() -> None:
    document = page()
    expected = {
        ("name", "description"): "Portico gives apps reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.",
        ("property", "og:title"): "Portico — your local apps, through a private door",
        ("property", "og:type"): "website",
        ("property", "og:url"): "https://chrisbanes.github.io/portico/",
        ("property", "og:image"): "https://chrisbanes.github.io/portico/assets/og.png",
        ("name", "twitter:card"): "summary_large_image",
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


def indentation(line: str) -> int:
    return len(line) - len(line.lstrip())


def mapping_has(lines: list[str], key: str, expected: tuple[str, ...]) -> bool:
    for index, line in enumerate(lines):
        if line.strip() != key:
            continue
        base = indentation(line)
        nested: list[str] = []
        for child in lines[index + 1 :]:
            if indentation(child) <= base:
                break
            nested.append(child.strip())
        if all(value in nested for value in expected):
            return True
    return False


def job_blocks(lines: list[str]) -> dict[str, list[str]]:
    starts = [
        (index, match.group(1))
        for index, line in enumerate(lines)
        if indentation(line) == 2 and (match := re.fullmatch(r"\s{2}([A-Za-z0-9_-]+):", line))
    ]
    return {
        name: lines[index + 1 : starts[position + 1][0] if position + 1 < len(starts) else len(lines)]
        for position, (index, name) in enumerate(starts)
    }


def has_pinned_action(lines: list[str], action: str, sha: str) -> bool:
    return any(re.fullmatch(rf"\s*-\s+uses:\s*{re.escape(action)}@{sha}", line) for line in lines)


def has_verifier_command(lines: list[str]) -> bool:
    command = r"python3 Scripts/verify-website\.py(?:\s+\S+)*"
    return any(
        re.fullmatch(rf"\s*-\s+run:\s*{command}", line)
        or re.fullmatch(rf"\s+{command}", line)
        for line in lines
    )


def has_pages_environment(lines: list[str]) -> bool:
    return any(line.strip() == "environment: github-pages" for line in lines) or mapping_has(lines, "environment:", ("name: github-pages",))


def active_if_lines(lines: list[str]) -> list[str]:
    return [line.strip() for line in lines if re.fullmatch(r"(?:-\s+)?if:\s*.+", line.strip())]


def validate_pages_workflow(workflow: str) -> None:
    lines = active_yaml_lines(workflow)
    require(mapping_has(lines, "on:", ("pull_request:", "workflow_dispatch:")), "Pages workflow missing PR/manual triggers")
    require(mapping_has(lines, "permissions:", ("contents: read", "pages: write", "id-token: write")), "Pages workflow missing required permissions")
    blocks = job_blocks(lines)
    require("build" in blocks, "Pages workflow missing build job")
    require("deploy" in blocks, "Pages workflow missing deploy job")
    build = blocks["build"]
    deploy = blocks["deploy"]
    require(not active_if_lines(build), "Pages workflow build job must not be conditional")
    require(
        active_if_lines(deploy) == ["if: github.event_name != 'pull_request' && github.ref == 'refs/heads/main'"],
        "Pages workflow deploy job must have only the required condition",
    )
    require(has_verifier_command(build), "Pages workflow missing verifier command in build")
    require(has_pinned_action(build, "actions/configure-pages", "983d7736d9b0ae728b81ab479565c72886d7745b"), "Pages workflow missing pinned configure-pages in build")
    require(has_pinned_action(build, "actions/upload-pages-artifact", "7b1f4a764d45c48632c6b24a0339c27f5614fb0b"), "Pages workflow missing pinned upload-pages-artifact in build")
    require(any(line.strip() == "path: website" for line in build), "Pages workflow missing website artifact path in build")
    require(has_pages_environment(deploy), "Pages workflow missing github-pages environment in deploy")
    require(has_pinned_action(deploy, "actions/deploy-pages", "d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e"), "Pages workflow missing pinned deploy-pages in deploy")
    for line in lines:
        match = re.fullmatch(r"\s*-\s+uses:\s*(\S+)", line)
        if match:
            require(re.fullmatch(r"[^@\s]+@[0-9a-f]{40}", match.group(1)) is not None, "Pages workflow has unpinned action")


def pages() -> None:
    candidates = list((ROOT / ".github/workflows").glob("*.y*ml"))
    require(candidates, "missing Pages workflow")
    failures: list[AssertionError] = []
    for candidate in candidates:
        try:
            validate_pages_workflow(candidate.read_text(encoding="utf-8"))
            return
        except AssertionError as error:
            failures.append(error)
    raise failures[-1]


CHECKS = {"structure": structure, "styles": styles, "behavior": behavior, "metadata": metadata, "pages": pages}


def main(arguments: list[str]) -> int:
    requested = arguments[0] if arguments else "all"
    if requested == "all":
        modes = CHECKS.items()
    elif requested in CHECKS:
        modes = [(requested, CHECKS[requested])]
    else:
        print(f"unknown mode: {requested}", file=sys.stderr)
        return 2
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
