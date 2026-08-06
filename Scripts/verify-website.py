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


def pages() -> None:
    candidates = [
        candidate
        for candidate in (ROOT / ".github/workflows").glob("*.y*ml")
        if "github-pages" in candidate.read_text(encoding="utf-8")
    ]
    require(candidates, "missing Pages workflow")
    workflow = "\n".join(candidate.read_text(encoding="utf-8") for candidate in candidates)
    for expected in ("pull_request:", "workflow_dispatch:", "permissions:", "python3 Scripts/verify-website.py", "path: website", "environment: github-pages"):
        require(expected in workflow, f"Pages workflow missing {expected}")
    for permission in ("contents: read", "pages: write", "id-token: write"):
        require(permission in workflow, f"Pages workflow missing {permission} permission")
    pins = {
        "actions/configure-pages": "983d7736d9b0ae728b81ab479565c72886d7745b",
        "actions/upload-pages-artifact": "7b1f4a764d45c48632c6b24a0339c27f5614fb0b",
        "actions/deploy-pages": "d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e",
    }
    for action, sha in pins.items():
        matches = re.findall(rf"uses:\s*{re.escape(action)}@([^\s#]+)", workflow)
        require(matches, f"Pages workflow missing {action}")
        require(all(match == sha for match in matches), f"Pages workflow has unpinned {action}")
    all_pins = re.findall(r"uses:\s*[^\s@]+@([^\s#]+)", workflow)
    require(all(re.fullmatch(r"[0-9a-f]{40}", pin) for pin in all_pins), "Pages workflow has unpinned action")


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
