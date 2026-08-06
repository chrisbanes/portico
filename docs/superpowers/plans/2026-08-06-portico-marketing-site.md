# Portico Marketing Site Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Build a distinctive, accessible Portico marketing page and a verified GitHub Pages publishing workflow.

**Architecture:** Add a dependency-free static site beneath website/ with semantic HTML, one responsive stylesheet, and one progressive-enhancement script. Add a Python standard-library verifier that treats the approved content, accessibility, local-asset, social-card, and Pages-workflow requirements as executable contracts.

**Tech Stack:** HTML5, CSS, vanilla JavaScript, Python 3 standard library, GitHub Pages, GitHub Actions

---

## File map

- Create Scripts/verify-website.py: page, asset, metadata, and workflow contract checks.
- Create website/index.html: marketing content and semantic native-app preview.
- Create website/styles.css: brand tokens, layout, responsiveness, focus, and reduced motion.
- Create website/script.js: progressive mobile navigation and scroll reveals.
- Create website/.nojekyll: disable Jekyll processing.
- Create website/assets/og.png: 1200 by 630 Portico social card.
- Create .github/workflows/pages.yml: validate and deploy website/ through Pages.

Do not modify Portico/, helper/, the Xcode project specification, or the existing Swift and Go CI workflow.

### Task 1: Add the static-site contract verifier

**Files:**

- Create: Scripts/verify-website.py

- [ ] **Step 1: Add the verifier**

Create Scripts/verify-website.py:

~~~python
#!/usr/bin/env python3

from __future__ import annotations

import re
import struct
import sys
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parents[1]
WEB = ROOT / "website"
INDEX = WEB / "index.html"
STYLES = WEB / "styles.css"
SCRIPT = WEB / "script.js"
CARD = WEB / "assets" / "og.png"
WORKFLOW = ROOT / ".github" / "workflows" / "pages.yml"


class Page(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.ids: set[str] = set()
        self.tags: list[str] = []
        self.links: list[dict[str, str]] = []
        self.resources: list[dict[str, str]] = []
        self.scripts: list[dict[str, str]] = []
        self.images: list[dict[str, str]] = []
        self.meta: dict[str, str] = {}
        self.h1: list[str] = []
        self._in_h1 = False
        self._h1_text: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        values = {key: value or "" for key, value in attrs}
        self.tags.append(tag)
        if element_id := values.get("id"):
            self.ids.add(element_id)
        if tag == "a":
            self.links.append(values)
        elif tag == "link":
            self.resources.append(values)
        elif tag == "script":
            self.scripts.append(values)
        elif tag == "img":
            self.images.append(values)
        elif tag == "meta":
            key = values.get("property") or values.get("name")
            if key:
                self.meta[key] = values.get("content", "")
        elif tag == "h1":
            self._in_h1 = True
            self._h1_text = []

    def handle_data(self, data: str) -> None:
        if self._in_h1:
            self._h1_text.append(data)

    def handle_endtag(self, tag: str) -> None:
        if tag == "h1" and self._in_h1:
            self.h1.append(" ".join(" ".join(self._h1_text).split()))
            self._in_h1 = False


def fail(message: str) -> None:
    raise AssertionError(message)


def read(path: Path) -> str:
    if not path.is_file():
        fail(f"Missing required file: {path.relative_to(ROOT)}")
    return path.read_text(encoding="utf-8")


def page() -> tuple[str, Page]:
    document = read(INDEX)
    parser = Page()
    parser.feed(document)
    return document, parser


def tokens(content: str, required: tuple[str, ...], label: str) -> None:
    missing = [value for value in required if value not in content]
    if missing:
        fail(f"{label} is missing: {', '.join(missing)}")


def structure() -> None:
    document, parser = page()
    required_ids = {
        "main-content",
        "how-it-works",
        "app-preview",
        "features",
        "architecture",
        "install",
    }
    if missing := sorted(required_ids - parser.ids):
        fail(f"Missing section IDs: {', '.join(missing)}")
    for landmark in ("header", "nav", "main", "footer"):
        if landmark not in parser.tags:
            fail(f"Missing {landmark} landmark")
    tokens(
        document,
        (
            'lang="en"',
            'class="skip-link"',
            'aria-controls="site-nav"',
            'aria-expanded="false"',
            "Your local apps,",
            "through a private door.",
            "Preview instructions",
            "selected working name and has not yet been legally cleared",
            "https://github.com/chrisbanes/portico",
        ),
        "HTML",
    )
    if parser.h1 != ["Your local apps, through a private door."]:
        fail("Expected exactly one approved h1")
    hrefs = {link.get("href", "") for link in parser.links}
    for href in ("#how-it-works", "#features", "#install"):
        if href not in hrefs:
            fail(f"Missing navigation link {href}")
    sheets = {
        link.get("href", "")
        for link in parser.resources
        if link.get("rel") == "stylesheet"
    }
    if sheets != {"styles.css"}:
        fail("The only stylesheet must be local styles.css")
    if {item.get("src", "") for item in parser.scripts} != {"script.js"}:
        fail("The only script must be local script.js")
    for path in (STYLES, SCRIPT, WEB / ".nojekyll"):
        if not path.is_file():
            fail(f"Missing local site file: {path.relative_to(ROOT)}")
    for asset in [*parser.images, *parser.scripts]:
        parsed = urlparse(asset.get("src", ""))
        if parsed.scheme or parsed.netloc:
            fail("Remote runtime assets are not allowed")


def styles() -> None:
    content = read(STYLES)
    tokens(
        content,
        (
            "--limestone:",
            "--ink:",
            "--oxidized:",
            "--coral:",
            ".skip-link",
            ":focus-visible",
            ".hero-portal",
            ".route-flow",
            ".app-window",
            ".feature-grid",
            "@media (max-width: 760px)",
            "@media (prefers-reduced-motion: reduce)",
            "overflow-x: hidden",
        ),
        "CSS",
    )


def behavior() -> None:
    content = read(SCRIPT)
    tokens(
        content,
        (
            "document.documentElement.classList.add",
            "aria-expanded",
            "Escape",
            "IntersectionObserver",
            "prefers-reduced-motion",
        ),
        "JavaScript",
    )


def dimensions(path: Path) -> tuple[int, int]:
    data = path.read_bytes()
    if len(data) < 24 or data[:8] != b"\x89PNG\r\n\x1a\n":
        fail("Social card must be a PNG")
    return struct.unpack(">II", data[16:24])


def metadata() -> None:
    _, parser = page()
    expected = {
        "description": "Portico gives apps reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.",
        "og:title": "Portico — your local apps, through a private door",
        "og:type": "website",
        "og:url": "https://chrisbanes.github.io/portico/",
        "og:image": "https://chrisbanes.github.io/portico/assets/og.png",
        "twitter:card": "summary_large_image",
    }
    for key, value in expected.items():
        if parser.meta.get(key) != value:
            fail(f"Metadata {key} must equal {value!r}")
    if not CARD.is_file():
        fail("Missing website/assets/og.png")
    if dimensions(CARD) != (1200, 630):
        fail("Social card must be exactly 1200 by 630 pixels")


def pages() -> None:
    content = read(WORKFLOW)
    tokens(
        content,
        (
            "name: Pages",
            "pull_request:",
            "workflow_dispatch:",
            "pages: write",
            "id-token: write",
            "python3 Scripts/verify-website.py",
            "path: website",
            "name: github-pages",
            "actions/configure-pages@983d7736d9b0ae728b81ab479565c72886d7745b",
            "actions/upload-pages-artifact@7b1f4a764d45c48632c6b24a0339c27f5614fb0b",
            "actions/deploy-pages@d6db90164ac5ed86f2b6aed7e0febac5b3c0c03e",
        ),
        "Pages workflow",
    )
    if re.search(r"uses:\s+[^@\s]+@(?![0-9a-f]{40}(?:\s|$))", content):
        fail("Workflow actions must use immutable commits")


CHECKS = {
    "structure": structure,
    "styles": styles,
    "behavior": behavior,
    "metadata": metadata,
    "pages": pages,
}


def main() -> int:
    requested = sys.argv[1:] or list(CHECKS)
    if unknown := [name for name in requested if name not in CHECKS]:
        print(f"Unknown validation mode: {', '.join(unknown)}", file=sys.stderr)
        return 2
    try:
        for name in requested:
            CHECKS[name]()
            print(f"PASS {name}")
    except AssertionError as error:
        print(f"FAIL {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
~~~

- [ ] **Step 2: Run the first contract and verify it fails**

Run:

~~~bash
python3 Scripts/verify-website.py structure
~~~

Expected: exit 1 with “Missing required file: website/index.html”.

Do not commit the verifier while red. Task 2 supplies the semantic page and the first green commit.

### Task 2: Build the semantic marketing page

**Files:**

- Create: website/index.html
- Create: website/styles.css
- Create: website/script.js
- Create: website/.nojekyll
- Test: Scripts/verify-website.py

- [ ] **Step 1: Create the site surface**

Create website/assets/. Add empty website/.nojekyll, website/styles.css, and website/script.js with apply_patch.

- [ ] **Step 2: Add the semantic document**

Create website/index.html. Use the exact metadata from the metadata() check. The body must use this exact semantic skeleton and copy:

~~~html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="description" content="Portico gives apps reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.">
    <meta property="og:title" content="Portico — your local apps, through a private door">
    <meta property="og:description" content="Portico gives apps reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.">
    <meta property="og:type" content="website">
    <meta property="og:url" content="https://chrisbanes.github.io/portico/">
    <meta property="og:image" content="https://chrisbanes.github.io/portico/assets/og.png">
    <meta name="twitter:card" content="summary_large_image">
    <meta name="theme-color" content="#e9e0cf">
    <title>Portico — private HTTPS doorways for your apps</title>
    <link rel="stylesheet" href="styles.css">
    <script src="script.js" defer></script>
  </head>
  <body>
    <a class="skip-link" href="#main-content">Skip to content</a>
    <header class="site-header">
      <a class="wordmark" href="#top" aria-label="Portico home">
        <span class="wordmark-mark" aria-hidden="true"></span><span>Portico</span>
      </a>
      <button class="nav-toggle" type="button" aria-controls="site-nav" aria-expanded="false">
        <span class="visually-hidden">Menu</span><span class="nav-toggle-mark" aria-hidden="true"></span>
      </button>
      <nav id="site-nav" class="site-nav" aria-label="Primary navigation">
        <a href="#how-it-works">How it works</a>
        <a href="#features">Features</a>
        <a href="#install">Install</a>
        <a class="nav-github" href="https://github.com/chrisbanes/portico">GitHub <span aria-hidden="true">↗</span></a>
      </nav>
    </header>
    <main id="main-content">
      <section id="top" class="hero section-shell">
        <div class="hero-copy" data-reveal>
          <p class="eyebrow">Private HTTPS for apps your Mac can reach</p>
          <h1>Your local apps,<br>through a private door.</h1>
          <p class="hero-intro">Portico is a native macOS menu-bar app that gives services reachable from your Mac stable, private HTTPS addresses on your Tailscale tailnet.</p>
          <div class="hero-actions">
            <a class="button button-primary" href="#install">See installation</a>
            <a class="button button-secondary" href="https://github.com/chrisbanes/portico">View on GitHub <span aria-hidden="true">↗</span></a>
          </div>
        </div>
        <div class="hero-visual" data-reveal>
          <div class="hero-portal">
            <div class="route-flow" aria-label="A local app passing through a Portal">
              <div class="route-card"><span>Local App</span><code>127.0.0.1:8787</code></div>
              <div class="route-line" aria-hidden="true"><span></span></div>
              <div class="route-card"><span>Private HTTPS</span><code>hermes.&lt;tailnet&gt;.ts.net</code></div>
            </div>
            <div class="portal-threshold"><span>One app</span><strong>One Portal</strong><span>One durable address</span></div>
          </div>
        </div>
      </section>
      <section id="how-it-works" class="steps section-shell">
        <header class="section-heading" data-reveal><p class="eyebrow">How it works</p><h2>From a port on your Mac<br>to a URL on your tailnet.</h2></header>
        <ol class="step-list">
          <li data-reveal><span>01</span><h3>Choose an app</h3><p>Select a detected Local App, enter a local port, or point Portico at an HTTP or HTTPS service your Mac can reach.</p></li>
          <li data-reveal><span>02</span><h3>Create a Portal</h3><p>Choose its permanent Portal Name, then authenticate the independent Tailscale node in your browser.</p></li>
          <li data-reveal><span>03</span><h3>Use the private URL</h3><p>Copy the HTTPS Portal URL and open the app from any permitted device on your tailnet.</p></li>
        </ol>
      </section>
      <section id="app-preview" class="product section-shell">
        <div class="product-copy" data-reveal>
          <p class="eyebrow">Native by design</p><h2>A clear front door for every Portal.</h2>
          <p>Portico keeps identity, connection state, and destination health separate, so “online” never hides an unreachable app.</p>
        </div>
        <div class="app-stage" data-reveal>
          <div class="app-window" aria-label="Example Portico management window">
            <div class="window-chrome" aria-hidden="true"><i></i><i></i><i></i><strong>Portico</strong></div>
            <div class="window-body">
              <aside class="app-sidebar" aria-label="Portal list">
                <h3>Portico</h3>
                <p>Overview</p>
                <p class="selected"><span class="status-dot" aria-hidden="true"></span><strong>hermes</strong><small>Online</small></p>
                <p><span class="status-dot stopped" aria-hidden="true"></span><strong>preview</strong><small>Stopped</small></p>
              </aside>
              <div class="app-detail">
                <header><span>PORTAL</span><h3>hermes</h3></header>
                <section><h4>Identity</h4><dl><div><dt>Portal Name</dt><dd>hermes</dd></div><div><dt>Assigned Name</dt><dd>hermes</dd></div></dl></section>
                <section><h4>Portal State</h4><dl><div><dt>Desired State</dt><dd>Enabled</dd></div><div><dt>Tailscale</dt><dd>Online</dd></div><div><dt>Local App</dt><dd>Reachable</dd></div></dl></section>
                <section class="app-route"><h4>Portal URL and destination</h4><code>https://hermes.example-tailnet.ts.net</code><span aria-hidden="true">↓</span><code>http://127.0.0.1:8787</code></section>
              </div>
            </div>
          </div>
        </div>
      </section>
      <section id="features" class="features section-shell">
        <header class="section-heading" data-reveal><p class="eyebrow">Built for the tailnet you already have</p><h2>Private access,<br>without the public detour.</h2></header>
        <div class="feature-grid">
          <article class="feature feature-wide" data-reveal><span>A</span><h3>Durable private HTTPS</h3><p>Give an app a memorable Portal URL that retains its identity when you change the service behind it.</p><code>https://hermes.&lt;tailnet&gt;.ts.net</code></article>
          <article class="feature feature-dark" data-reveal><span>B</span><h3>Independent Portals</h3><p>Each Portal is its own Tailscale node with its own durable state, lifecycle, and Portal Destination.</p></article>
          <article class="feature" data-reveal><span>C</span><h3>Browser authentication with no auth keys</h3><p>Authenticate each Portal in the browser without handling auth keys.</p></article>
          <article class="feature" data-reveal><span>D</span><h3>Local or remote</h3><p>Route to a Local App on the Mac or a Remote App available through the Mac’s normal network.</p></article>
          <article class="feature" data-reveal><span>E</span><h3>Native controls</h3><p>Create, inspect, stop, repoint, and remove Portals from a focused macOS menu-bar app.</p></article>
          <article class="feature" data-reveal><span>F</span><h3>Honest status</h3><p>Desired state, tailnet connectivity, and destination reachability remain separate and readable.</p></article>
        </div>
      </section>
      <section id="architecture" class="architecture">
        <div class="architecture-grid section-shell">
          <div data-reveal><p class="eyebrow">Under the threshold</p><h2>Your Mac stays the route.</h2><p>Portico does not rename, retag, or proxy through your Mac’s existing Tailscale node. Every Portal joins the tailnet as an independent user-owned node and forwards to exactly one configured destination.</p><a href="https://github.com/chrisbanes/portico">Explore the architecture <span aria-hidden="true">→</span></a></div>
          <div class="architecture-route" data-reveal aria-label="Portal request route"><div><span>Tailnet device</span><strong>HTTPS request</strong></div><i aria-hidden="true"></i><div class="route-node"><span>Independent node</span><strong>Portal</strong></div><i aria-hidden="true"></i><div><span>Mac-reachable service</span><strong>Portal Destination</strong></div></div>
        </div>
      </section>
      <section id="install" class="install section-shell">
        <div class="install-card" data-reveal>
          <div><p class="eyebrow">Preview instructions</p><h2>Open the door.</h2><p>Release packaging is still being prepared. These steps show the intended installation flow, not a currently published build.</p></div>
          <ol><li><span>1</span><div><strong>Preview availability</strong><p>Preview availability will be announced here.</p></div></li><li><span>2</span><div><strong>Installation details</strong><p>Installation details will accompany an announced preview.</p></div></li><li><span>3</span><div><strong>Browser authentication</strong><p>The planned flow uses browser authentication.</p></div></li></ol>
          <p class="install-note">Preview availability and installation details are still being prepared. This is placeholder guidance, not a release or download.</p>
        </div>
      </section>
    </main>
    <footer class="site-footer section-shell">
      <a class="wordmark" href="#top"><span class="wordmark-mark" aria-hidden="true"></span><span>Portico</span></a>
      <p>Portico is the selected working name and has not yet been legally cleared.</p>
      <a href="https://github.com/chrisbanes/portico">GitHub <span aria-hidden="true">↗</span></a>
    </footer>
  </body>
</html>
~~~

- [ ] **Step 3: Run the structure contract**

Run:

~~~bash
python3 Scripts/verify-website.py structure
~~~

Expected: PASS structure.

- [ ] **Step 4: Commit the green semantic slice**

~~~bash
git add Scripts/verify-website.py website
git commit -m "Add Portico marketing site structure"
~~~

### Task 3: Implement the Private doorway visual system

**Files:**

- Modify: website/styles.css
- Test: Scripts/verify-website.py
- Reference: docs/superpowers/specs/2026-08-05-portico-marketing-site-design.md

- [ ] **Step 1: Verify the visual contract is red**

Run python3 Scripts/verify-website.py styles.

Expected: exit 1 beginning with “FAIL CSS is missing”.

- [ ] **Step 2: Define the exact brand foundation**

Start website/styles.css with:

~~~css
:root {
  --limestone: #e9e0cf;
  --limestone-light: #f6f1e7;
  --paper: #fffdf8;
  --ink: #15211d;
  --ink-soft: #43504a;
  --oxidized: #245d4a;
  --oxidized-dark: #113c31;
  --oxidized-light: #a9c7b5;
  --coral: #e56f51;
  --line: rgba(21, 33, 29, 0.18);
  --display: Georgia, "Times New Roman", serif;
  --sans: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  color: var(--ink);
  background: var(--limestone);
  font-family: var(--sans);
  font-synthesis: none;
  scroll-behavior: smooth;
}

* { box-sizing: border-box; }
html { scroll-padding-top: 6rem; }
body {
  margin: 0;
  min-width: 320px;
  overflow-x: hidden;
  background:
    linear-gradient(90deg, transparent 0 7%, rgba(21, 33, 29, 0.05) 7% calc(7% + 1px), transparent calc(7% + 1px)),
    var(--limestone);
  line-height: 1.55;
}
a { color: inherit; text-underline-offset: 0.2em; }
code { font-family: "SFMono-Regular", Consolas, monospace; }
:focus-visible { outline: 3px solid var(--coral); outline-offset: 4px; }
.section-shell, .site-header {
  width: min(1180px, calc(100% - 3rem));
  margin-inline: auto;
}
~~~

- [ ] **Step 3: Implement every component selector**

Add focused blocks for the following selectors. These values are requirements, not suggestions:

- .skip-link: fixed above the viewport until focused, then visible at top-left.
- .site-header: sticky pill, translucent limestone, 4.25rem minimum height.
- .wordmark-mark: CSS-only 1.55rem by 1.8rem arched doorway.
- .site-nav and .nav-toggle: desktop links; toggle hidden until the 760px breakpoint.
- h1 and h2: Georgia display face, 0.95 line height, -0.055em tracking.
- h1: clamp from 4rem to 7.25rem; mobile clamp from 3.4rem to 5.3rem.
- .hero: two-column desktop grid and one-column below 980px.
- .hero-portal: at least 570px tall, 17rem top radii, oxidized-green gradient.
- .route-flow and .route-card: centered vertical route with readable monospace strings.
- .route-line span: coral request pulse using a route-pulse keyframe.
- .step-list: three equal columns, then one column below 760px.
- .product: explanatory column plus app-stage column.
- .app-window: native neutral window with chrome, 10.5rem sidebar, detail cards, and semantic status text.
- .feature-grid: three desktop columns with .feature-wide spanning two; two columns below 980px; one below 760px.
- .architecture: full-width oxidized-dark section.
- .architecture-route: five-column request flow, one column on mobile.
- .install-card: two columns on desktop, one on mobile.
- .site-footer: three columns on desktop, one centered column on mobile.
- .enhanced [data-reveal]: opacity and 24px vertical transition; .is-visible restores the final state.

Implement those requirements with these declarations between the foundation and the motion ending:

~~~css
.skip-link { position: fixed; z-index: 100; top: .75rem; left: .75rem; padding: .7rem 1rem; transform: translateY(-160%); border-radius: 999px; background: var(--ink); color: white; }
.skip-link:focus { transform: none; }
.visually-hidden { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
.site-header { position: sticky; z-index: 50; top: 1rem; display: flex; align-items: center; justify-content: space-between; min-height: 4.25rem; padding: .65rem .75rem .65rem 1rem; border: 1px solid var(--line); border-radius: 999px; background: rgba(246, 241, 231, .86); backdrop-filter: blur(18px); }
.wordmark { display: inline-flex; align-items: center; gap: .7rem; font-weight: 800; text-decoration: none; }
.wordmark-mark { width: 1.55rem; height: 1.8rem; border: 2px solid currentColor; border-bottom-width: 4px; border-radius: 1rem 1rem .2rem .2rem; background: var(--oxidized-light); box-shadow: inset 0 -.35rem 0 var(--oxidized); }
.site-nav { display: flex; align-items: center; gap: clamp(.7rem, 2vw, 1.7rem); }
.site-nav a { min-height: 44px; padding: .65rem .25rem; font-size: .88rem; font-weight: 700; text-decoration: none; }
.nav-github { padding-inline: 1rem !important; border-radius: 999px; background: var(--ink); color: white; }
.nav-toggle { display: none; border: 0; border-radius: 50%; background: var(--ink); color: white; }
.nav-toggle-mark, .nav-toggle-mark::before, .nav-toggle-mark::after { display: block; width: 1.1rem; height: 2px; background: currentColor; }
.nav-toggle-mark { position: relative; }
.nav-toggle-mark::before, .nav-toggle-mark::after { position: absolute; left: 0; content: ""; }
.nav-toggle-mark::before { top: -5px; }
.nav-toggle-mark::after { top: 5px; }
.hero { display: grid; grid-template-columns: minmax(0, 1.08fr) minmax(390px, .92fr); gap: clamp(3rem, 8vw, 7rem); align-items: center; min-height: calc(100svh - 6.5rem); padding-block: clamp(5rem, 10vw, 9rem); }
.eyebrow { margin: 0 0 1.1rem; color: var(--oxidized); font-size: .74rem; font-weight: 850; letter-spacing: .16em; text-transform: uppercase; }
h1, h2 { font-family: var(--display); font-weight: 500; letter-spacing: -.055em; line-height: .95; }
h1 { max-width: 12ch; margin-bottom: 1.7rem; font-size: clamp(4rem, 7.5vw, 7.25rem); }
h2 { margin-bottom: 1.4rem; font-size: clamp(2.8rem, 5.5vw, 5.2rem); }
.hero-intro { max-width: 39rem; color: var(--ink-soft); font-size: clamp(1.05rem, 1.6vw, 1.3rem); }
.hero-actions { display: flex; flex-wrap: wrap; gap: .8rem; margin-top: 2rem; }
.button { display: inline-flex; align-items: center; justify-content: center; min-height: 48px; padding: .8rem 1.2rem; border: 1px solid var(--ink); border-radius: 999px; font-weight: 800; text-decoration: none; }
.button-primary { background: var(--ink); color: white; }
.hero-portal { position: relative; min-height: 570px; overflow: hidden; border-radius: 17rem 17rem 1.25rem 1.25rem; background: radial-gradient(circle at 50% 20%, rgba(169, 199, 181, .42), transparent 34%), linear-gradient(155deg, var(--oxidized), var(--oxidized-dark) 68%); box-shadow: 0 28px 80px rgba(25, 42, 35, .18); color: white; }
.route-flow { display: grid; place-items: center; align-content: center; min-height: 490px; padding: 4rem 2rem 2rem; }
.route-card { width: min(100%, 24rem); padding: 1rem 1.1rem; border: 1px solid rgba(255, 255, 255, .25); border-radius: 1rem; background: rgba(10, 26, 21, .5); }
.route-card span { color: var(--oxidized-light); font-size: .7rem; font-weight: 850; letter-spacing: .11em; text-transform: uppercase; }
.route-card code { display: block; margin-top: .3rem; overflow-wrap: anywhere; color: white; }
.route-line { position: relative; width: 1px; height: 5rem; overflow: hidden; background: rgba(255, 255, 255, .25); }
.route-line span { position: absolute; top: -1rem; left: -2px; width: 5px; height: 1rem; border-radius: 999px; background: var(--coral); animation: route-pulse 2.4s ease-in-out infinite; }
@keyframes route-pulse { to { transform: translateY(7rem); } }
.portal-threshold { position: absolute; inset: auto 0 0; display: flex; justify-content: space-between; gap: 1rem; padding: 1.15rem 1.4rem; background: rgba(8, 28, 22, .84); font-size: .7rem; text-transform: uppercase; }
.portal-threshold strong { color: var(--coral); }
.steps, .features, .install { padding-block: clamp(6rem, 12vw, 10rem); }
.section-heading { margin-bottom: clamp(3rem, 7vw, 5.5rem); padding-bottom: 1.4rem; border-bottom: 1px solid var(--line); }
.step-list { display: grid; grid-template-columns: repeat(3, 1fr); margin: 0; padding: 0; list-style: none; }
.step-list li { min-height: 18rem; padding: 1.5rem 2rem 1.5rem 0; border-right: 1px solid var(--line); }
.step-list li + li { padding-left: 2rem; }
.step-list li:last-child { border-right: 0; }
.step-list h3 { margin: 4rem 0 .75rem; font-size: 1.45rem; }
.product { display: grid; grid-template-columns: minmax(260px, .55fr) minmax(0, 1.45fr); gap: clamp(3rem, 8vw, 7rem); align-items: center; padding-block: clamp(5rem, 10vw, 8rem); }
.app-stage { padding: clamp(1rem, 4vw, 3rem); border-radius: 2rem; background: var(--oxidized); }
.app-window { overflow: hidden; border: 1px solid rgba(21, 33, 29, .24); border-radius: 1rem; background: #f4f2ed; box-shadow: 0 30px 55px rgba(9, 35, 27, .28); font-size: .72rem; }
.window-chrome { display: flex; align-items: center; gap: .4rem; height: 2.4rem; padding: 0 .8rem; background: #e8e5df; }
.window-chrome i { width: .62rem; height: .62rem; border-radius: 50%; background: #c7c3bc; }
.window-chrome i:first-child { background: #ff5f57; }
.window-chrome i:nth-child(2) { background: #febc2e; }
.window-chrome i:nth-child(3) { background: #28c840; }
.window-chrome strong { margin: auto; transform: translateX(-1.8rem); }
.window-body { display: grid; grid-template-columns: 10.5rem 1fr; min-height: 31rem; }
.app-sidebar { padding: 1rem .55rem; border-right: 1px solid #d8d4cc; background: #e4e2dc; }
.app-sidebar p { display: flex; align-items: center; gap: .55rem; min-height: 2.7rem; margin: 0; padding: .5rem .65rem; border-radius: .45rem; }
.app-sidebar .selected { background: #d0cec8; }
.app-sidebar small { display: block; color: #6d746e; }
.status-dot { width: .55rem; height: .55rem; border-radius: 50%; background: #2daf61; }
.status-dot.stopped { background: #92958f; }
.app-detail { padding: 1.5rem; }
.app-detail section { margin-top: 1rem; padding: .9rem 1rem; border: 1px solid #d6d3cc; border-radius: .6rem; background: rgba(255, 255, 255, .62); }
.app-detail dl, .app-detail dd { margin: 0; }
.app-detail dl div { display: flex; justify-content: space-between; gap: 1rem; padding-block: .28rem; }
.app-route { display: grid; gap: .35rem; overflow-wrap: anywhere; }
.feature-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1px; overflow: hidden; border: 1px solid var(--line); border-radius: 1.5rem; background: var(--line); }
.feature { min-height: 18rem; padding: 1.6rem; background: var(--limestone-light); }
.feature-wide { grid-column: span 2; }
.feature-dark { background: var(--ink); color: white; }
.feature h3 { margin: 4.5rem 0 .8rem; font-size: 1.45rem; }
.architecture { padding-block: clamp(6rem, 12vw, 10rem); background: var(--oxidized-dark); color: white; }
.architecture-grid { display: grid; grid-template-columns: .75fr 1.25fr; gap: clamp(3rem, 9vw, 8rem); align-items: center; }
.architecture-route { display: grid; grid-template-columns: 1fr auto 1fr auto 1fr; align-items: center; gap: .8rem; }
.architecture-route div { display: grid; align-content: center; min-height: 9rem; padding: 1rem; border: 1px solid rgba(255, 255, 255, .18); border-radius: .9rem; text-align: center; }
.architecture-route .route-node { border-color: var(--coral); border-radius: 4.5rem 4.5rem .9rem .9rem; }
.architecture-route i { width: 1.6rem; height: 1px; background: var(--coral); }
.install-card { display: grid; grid-template-columns: .8fr 1.2fr; gap: clamp(2.5rem, 8vw, 7rem); padding: clamp(2rem, 6vw, 5rem); border: 1px solid var(--line); border-radius: 1.75rem; background: var(--paper); }
.install-card ol { margin: 0; padding: 0; list-style: none; }
.install-card li { display: grid; grid-template-columns: 2.5rem 1fr; gap: 1rem; padding: 1.2rem 0; border-top: 1px solid var(--line); }
.install-note { grid-column: 2; padding: 1rem; border-radius: .7rem; background: #f2e8d6; color: var(--ink-soft); }
.site-footer { display: grid; grid-template-columns: 1fr 2fr 1fr; gap: 2rem; align-items: center; padding-block: 2.5rem; border-top: 1px solid var(--line); font-size: .78rem; }
.site-footer p { margin: 0; color: var(--ink-soft); text-align: center; }
.site-footer > a:last-child { justify-self: end; font-weight: 800; }

@media (max-width: 980px) {
  .hero, .product, .architecture-grid { grid-template-columns: 1fr; }
  .hero { min-height: auto; }
  .hero-visual { width: min(100%, 40rem); margin-inline: auto; }
  .feature-grid { grid-template-columns: repeat(2, 1fr); }
}
~~~

Use this exact motion and accessibility ending:

~~~css
.enhanced [data-reveal] {
  opacity: 0;
  transform: translateY(24px);
  transition: opacity 700ms ease, transform 700ms ease;
}
.enhanced [data-reveal].is-visible {
  opacity: 1;
  transform: none;
}

@media (max-width: 760px) {
  .section-shell, .site-header { width: min(100% - 1.5rem, 42rem); }
  .js .nav-toggle {
    display: grid;
    place-items: center;
    min-width: 44px;
    min-height: 44px;
  }
  .js .site-nav {
    position: absolute;
    top: calc(100% + 0.5rem);
    right: 0;
    left: 0;
    display: none;
  }
  .js .site-nav.is-open { display: grid; }
  .step-list, .feature-grid, .architecture-route, .install-card, .site-footer {
    grid-template-columns: 1fr;
  }
  .app-window { overflow-x: auto; }
  .window-body { grid-template-columns: 7.2rem minmax(19rem, 1fr); }
}

@media (prefers-reduced-motion: reduce) {
  :root { scroll-behavior: auto; }
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    animation-iteration-count: 1 !important;
    transition-duration: 0.01ms !important;
  }
  .enhanced [data-reveal] { opacity: 1; transform: none; }
}
~~~

- [ ] **Step 4: Verify and commit**

Run:

~~~bash
python3 Scripts/verify-website.py structure styles
git diff --check
~~~

Expected: PASS structure, PASS styles, and no diff errors.

Commit:

~~~bash
git add website/styles.css
git commit -m "Style Portico marketing site"
~~~

### Task 4: Add progressive behavior

**Files:**

- Modify: website/script.js
- Test: Scripts/verify-website.py

- [ ] **Step 1: Verify behavior is red**

Run python3 Scripts/verify-website.py behavior.

Expected: exit 1 beginning with “FAIL JavaScript is missing”.

- [ ] **Step 2: Add progressive enhancement**

Replace website/script.js:

~~~javascript
document.documentElement.classList.add("js");

const navigation = document.querySelector("#site-nav");
const toggle = document.querySelector(".nav-toggle");

const closeNavigation = () => {
  if (!navigation || !toggle) return;
  navigation.classList.remove("is-open");
  toggle.setAttribute("aria-expanded", "false");
};

if (navigation && toggle) {
  toggle.addEventListener("click", () => {
    const isOpen = navigation.classList.toggle("is-open");
    toggle.setAttribute("aria-expanded", String(isOpen));
  });
  navigation.addEventListener("click", (event) => {
    if (event.target.closest("a")) closeNavigation();
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closeNavigation();
      toggle.focus();
    }
  });
  window.matchMedia("(min-width: 761px)").addEventListener("change", (event) => {
    if (event.matches) closeNavigation();
  });
}

const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
const revealItems = document.querySelectorAll("[data-reveal]");

if (!reducedMotion.matches && "IntersectionObserver" in window) {
  document.documentElement.classList.add("enhanced");
  const observer = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.classList.add("is-visible");
        observer.unobserve(entry.target);
      }
    },
    { rootMargin: "0px 0px -8% 0px", threshold: 0.12 },
  );
  for (const item of revealItems) observer.observe(item);
}
~~~

- [ ] **Step 3: Verify and commit**

Run:

~~~bash
node --check website/script.js
python3 Scripts/verify-website.py behavior
~~~

Expected: node exits 0 and the verifier prints PASS behavior.

Commit:

~~~bash
git add website/script.js
git commit -m "Add progressive website enhancements"
~~~

### Task 5: Create the social preview

**Files:**

- Create: website/assets/og.png
- Test: Scripts/verify-website.py

- [ ] **Step 1: Verify metadata is red**

Run python3 Scripts/verify-website.py metadata.

Expected: exit 1 with “FAIL Missing website/assets/og.png”.

- [ ] **Step 2: Generate exactly one social card**

Read the imagegen skill and submit this brief:

~~~text
Create a complete 1200x630 landscape social preview card for Portico, a native macOS developer utility. Reuse the finished website’s warm limestone background (#e9e0cf), near-black ink (#15211d), oxidized green (#245d4a), and small coral accent (#e56f51). The distinctive motif is a tall rounded architectural doorway showing a private route from “127.0.0.1:8787” to “hermes.<tailnet>.ts.net”. Include only these exact prominent words: “Portico” and “Your local apps, through a private door.” Use editorial serif headline typography with restrained system-sans supporting text. Make the card spare, premium, legible at small unfurl sizes, and visually consistent with the website. Do not add logos, badges, people, devices, extra claims, release versions, or any other text.
~~~

Save the result as website/assets/og.png. Resize or center-crop without changing content if necessary. Do not request a second candidate unless the first has missing, incorrect, or invented text.

- [ ] **Step 3: Inspect, verify, and commit**

Inspect the PNG at original resolution. Confirm the Portico name, headline, and both route strings are exact; no company, version, download, availability, or privacy claim is invented.

Run python3 Scripts/verify-website.py metadata.

Expected: PASS metadata.

Commit:

~~~bash
git add website/assets/og.png
git commit -m "Add Portico social preview card"
~~~

### Task 6: Add GitHub Pages publishing

**Files:**

- Create: .github/workflows/pages.yml
- Test: Scripts/verify-website.py

- [ ] **Step 1: Verify Pages is red**

Run python3 Scripts/verify-website.py pages.

Expected: exit 1 with “FAIL Missing required file: .github/workflows/pages.yml”.

- [ ] **Step 2: Add the workflow**

Create .github/workflows/pages.yml:

~~~yaml
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
~~~

These immutable commits correspond to the first-party configure-pages v5, upload-pages-artifact v4, and deploy-pages v4 versions documented by GitHub on 2026-08-06.

- [ ] **Step 3: Run all nonvisual checks**

~~~bash
python3 Scripts/verify-website.py
node --check website/script.js
git diff --check
~~~

Expected verifier output:

~~~text
PASS structure
PASS styles
PASS behavior
PASS metadata
PASS pages
~~~

The node and git commands exit 0 without output.

- [ ] **Step 4: Commit**

~~~bash
git add .github/workflows/pages.yml
git commit -m "Add GitHub Pages publishing workflow"
~~~

### Task 7: Perform browser and final validation

**Files:**

- Review: website/index.html
- Review: website/styles.css
- Review: website/script.js
- Review: website/assets/og.png
- Review: .github/workflows/pages.yml
- Test: Scripts/verify-website.py

- [ ] **Step 1: Start a retained local server**

Run:

~~~bash
python3 -m http.server 4173 --directory website
~~~

Expected: serving on port 4173.

- [ ] **Step 2: Inspect desktop at approximately 1440 by 900**

Open http://127.0.0.1:4173/ and confirm:

- Hero headline and doorway route are above the fold without clipping.
- Sticky navigation is legible.
- The app window reads as a native product presentation.
- All six sections are visually distinct.
- Preview instructions do not offer a fake download.
- There is no missing asset or console error.

- [ ] **Step 3: Inspect mobile at approximately 390 by 844**

Confirm:

- No page-level horizontal scrolling.
- The 44-pixel menu control announces expanded state and closes with a link or Escape.
- Hero route remains legible.
- App preview preserves readable fields inside its bounded horizontal area.
- Features, architecture, installation, and footer become one column.

- [ ] **Step 4: Inspect keyboard and degradation behavior**

Confirm visible focus from skip link through navigation and calls to action. Activate the skip link. Emulate reduced motion and verify animation stops. Disable JavaScript and verify all navigation links and sections remain visible.

- [ ] **Step 5: Make only evidence-based fixes**

Patch only a concrete defect found above. Repeat the affected check and full verifier. If a fix was required:

~~~bash
git add website
git commit -m "Polish Portico website presentation"
~~~

Do not make an empty commit.

- [ ] **Step 6: Run final validation**

Stop the local server, then run:

~~~bash
python3 Scripts/verify-website.py
node --check website/script.js
git diff --check
git status --short
~~~

Expected: all five verifier modes pass; syntax and diff checks exit 0; only the implementation-plan file may remain uncommitted.

Deployment begins only after these commits reach main and the repository’s Pages source is configured for GitHub Actions. Do not change repository settings, push, or publish unless the user separately authorizes those external mutations.
