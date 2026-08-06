# Portico Marketing Site Design

**Status:** Approved

**Date:** 2026-08-05

## Goal

Create a polished, single-page marketing website for Portico that can be
published on GitHub Pages. The page should help developers understand the
product quickly, make the private-doorway metaphor memorable, and direct them
to placeholder installation guidance or the GitHub repository.

The site markets the app without presenting Portico as a finished public
release. Installation copy is intentionally illustrative until the release
process is defined.

## Audience

The primary audience is developers who run web services on a Mac and already
understand localhost, HTTPS, and Tailscale. Copy can use those terms directly,
but should explain Portico's product-specific language: Portal, Portal Name,
Assigned Name, Portal URL, and Portal Destination.

## Creative Direction

The visual direction is **Private doorway**: an editorial, architectural page
built around Portico's Portal metaphor.

- Warm limestone provides the main background.
- Near-black ink provides type and structural lines.
- Oxidized green is the primary brand accent.
- Coral is reserved for small operational status details.
- Rounded arch and doorway geometry is the distinctive recurring motif.
- Oversized editorial display type is paired with a restrained system sans
  serif for interface and supporting copy.
- Thin architectural rules, generous spacing, and subtle depth create a
  crafted feel without imitating generic macOS product pages.

The design uses CSS shapes and typography rather than decorative SVG artwork.
Motion is restrained to route-flow and entrance details and is removed when
the visitor prefers reduced motion.

## Page Structure

### Navigation

A compact sticky header contains the Portico wordmark, links to How it works,
Features, and Install, plus a link to
`https://github.com/chrisbanes/portico`. On small screens, the links collapse
into an accessible menu.

### Hero

The first viewport is a product manifesto rather than documentation.

- Headline: **Your local apps, through a private door.**
- Supporting copy: Portico is a native macOS menu-bar app that gives services
  reachable from the Mac stable, private HTTPS addresses on a Tailscale
  tailnet.
- Primary action: **See installation** and scroll to the install section.
- Secondary action: **View on GitHub**.
- Core visual transformation:
  `http://127.0.0.1:8787` to
  `https://hermes.<tailnet>.ts.net`.

The transformation sits inside or passes through the large doorway motif so
the metaphor explains the product before the visitor reads the detail.

### How it works

Three concise steps explain the daily flow:

1. **Choose an app.** Select a detected Local App or enter a Local or Remote
   App destination.
2. **Create a Portal.** Choose an immutable Portal Name and authenticate the
   independent Tailscale node.
3. **Use the private URL.** Copy a stable HTTPS Portal URL that is reachable on
   the tailnet.

### Native app preview

A realistic HTML and CSS composition represents the current Portico
management window. It uses existing product language and plausible sample
values:

- Portal Name: `hermes`
- Assigned Name: `hermes`
- Desired State: `Enabled`
- Tailscale: `Online`
- Local App: `Reachable`
- Portal URL: `https://hermes.example-tailnet.ts.net`
- Local App destination: `127.0.0.1:8787`

The preview shows the Overview and Portal sidebar relationship and a selected
Portal detail. It does not imply capabilities beyond the current app model.
The composition remains semantic HTML so text and status remain accessible.

### Features

A compact feature grid communicates:

- Stable private HTTPS addresses for services reachable from the Mac.
- Independent Portals with durable identities.
- Local App and Remote App destinations.
- Native menu-bar and management-window controls.
- Clear independent Enabled, Tailscale, and destination reachability states.
- Browser authentication with no auth keys required for the MVP model.

### Technical confidence

This section explains that every Portal is an independent Tailscale node and
Portico does not rename or proxy through the Mac's existing Tailscale node. A
small route diagram reinforces the flow from tailnet HTTPS through a Portal to
the configured destination.

The copy should remain concise and link interested visitors to the repository
for the deeper architecture.

### Installation

The installation section is visibly labelled **Preview instructions**. It
contains plausible placeholder steps:

1. Download the latest signed build.
2. Move Portico to Applications and open it.
3. Add a Portal and follow browser authentication.

A note states that release packaging and final installation instructions are
still being prepared. No fake release URL, package name, or version number is
used.

### Footer

The footer repeats the GitHub link and states that Portico is a selected
working name that has not yet been legally cleared. It avoids an invented
company name, copyright owner, support address, or social account.

## Implementation Architecture

The website lives in a new `website/` directory and uses:

- `index.html` for semantic structure and content;
- `styles.css` for the complete responsive visual system;
- `script.js` for progressive enhancement only;
- local raster assets, including a product-specific social preview image; and
- `.nojekyll` to keep GitHub Pages behavior predictable.

There is no framework, dependency installation, remote font, analytics,
cookie, form, persistence, or runtime API. The document remains complete and
usable when JavaScript is unavailable. JavaScript is limited to the mobile
navigation and presentational entrance behavior.

All asset references are relative so the site works at both a GitHub project
path such as `chrisbanes.github.io/portico/` and a future custom domain.

## GitHub Pages Publishing

A dedicated workflow in `.github/workflows/` uploads the `website/` directory
as the Pages artifact and deploys it with GitHub's supported Pages actions.
The workflow runs on changes to the website or the workflow itself and can be
started manually. Its permissions are limited to reading repository contents,
writing Pages, and obtaining the deployment identity token.

Publishing remains independent of the Swift and Go verification workflows.

## Accessibility and Responsive Behavior

- Use semantic landmarks and a logical heading hierarchy.
- Provide a skip link and visible keyboard focus.
- Keep all meaningful status available as text, never color alone.
- Maintain readable contrast across the limestone and green palette.
- Respect `prefers-reduced-motion` and avoid essential motion.
- Give the mobile navigation explicit expanded state and an accessible label.
- Use touch targets of at least 44 CSS pixels where controls are present.
- Reflow the hero, steps, app preview, and feature grid into one clear mobile
  column without horizontal page scrolling.
- Preserve app-preview readability at narrow widths through internal grouping
  rather than shrinking the entire composition.

## Error and Degradation Behavior

The page has no network-dependent runtime behavior. If JavaScript fails, all
sections and navigation links remain visible and usable. If entrance effects
are unsupported, content renders in its final state. Missing optional social
metadata does not affect the page itself.

Outbound links use normal browser behavior. The placeholder installation
section is explicit so visitors cannot confuse it with released instructions.

## Validation

Implementation is complete when:

- all local asset references resolve from a repository subpath;
- HTML landmarks, headings, links, and controls are structurally valid;
- CSS covers desktop and narrow mobile layouts without horizontal overflow;
- the script passes syntax checking and the site is complete without it;
- reduced-motion behavior removes nonessential animation;
- the GitHub Pages workflow parses and publishes only `website/`;
- the rendered page is inspected locally at representative desktop and mobile
  sizes; and
- unrelated Swift, Go, and documentation surfaces remain unchanged.

## Out of Scope

- A browser simulation of the full native app.
- Real downloads, release versions, Homebrew commands, or signing claims.
- Authentication, contact forms, newsletter capture, analytics, or cookies.
- Product documentation beyond the marketing-page narrative.
- Changes to the Portico macOS app or helper.
