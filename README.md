# Rodney: Chrome automation from the command line

> **This is [rodrigolive](https://github.com/rodrigolive)'s fork of [simonw/rodney](https://github.com/simonw/rodney).** It merges reviewed fixes and features from other forks that upstream hasn't taken yet. See [About this fork](#about-this-fork) for what changed, where it came from and how it was checked. `pip install rodney` / `uvx rodney` still install upstream; build from source to get this version.

[![PyPI](https://img.shields.io/pypi/v/rodney.svg)](https://pypi.org/project/rodney/)
[![Changelog](https://img.shields.io/github/v/release/simonw/rodney?include_prereleases&label=changelog)](https://github.com/simonw/rodney/releases)
[![Tests](https://github.com/simonw/rodney/actions/workflows/test.yml/badge.svg)](https://github.com/simonw/rodney/actions/workflows/test.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](https://github.com/simonw/rodney/blob/main/LICENSE)

A Go CLI tool that drives a persistent headless Chrome instance using the [rod](https://github.com/go-rod/rod) browser automation library. Each command connects to the same long-running Chrome process, making it easy to script multi-step browser interactions from shell scripts or interactive use.

## About this fork

### Why it exists

Upstream's last commit was on 2026-03-12. Since then, open PRs and bug reports (crashes on macOS, a `--show` fix that never got a release) have sat unmerged, and useful work has spread across dozens of forks. This fork pulls the best of that work into one tree. It is aimed at the way I use rodney: coding agents driving a headless Chrome on macOS, often several agents at once.

### How the forks were chosen and checked

1. I listed all 58 forks of simonw/rodney and compared each branch with upstream `main`. 46 branches had commits of their own. I read their logs and dropped duplicates, stale copies, personal tweaks and anything already fixed upstream.
2. Before merging, I read every added line of the shortlisted branches, looking specifically for:
   - network calls to anything other than the local Chrome DevTools endpoint;
   - code that downloads or runs other programs, obfuscated strings, and file writes or deletes outside rodney's own state directory;
   - new Go dependencies, which I checked against the Go module proxy and checksum database;
   - CI workflows that use secrets or publish anything, and README or help text written to steer AI agents.

   I also checked who wrote each commit: real, long-standing GitHub accounts versus anonymous identities.
3. Every branch is merged **by the exact commit SHA that was reviewed**, never by branch name, so anything pushed to those forks later cannot slip in. Each merge commit message says what the branch does, how conflicts were resolved and what the review found.

No malicious code turned up. Two branches had real safety problems; one was fixed after merging and the other was left out (see below).

### What was merged

| Source | Reviewed SHA | What it adds |
|---|---|---|
| [goeric, PR #54](https://github.com/simonw/rodney/pull/54) | `663fc04` | Fixes two crashes that take down the whole browser: `--single-process` is no longer used on macOS (any page touching the camera/mic API aborted Chromium), and unfinished Chromium experiments such as HistoryEmbeddings are switched off |
| [goeric, PR #55](https://github.com/simonw/rodney/pull/55) | `62b5bbf` | `rodney page N` now brings the tab to the front, so a following `screenshot` no longer times out (or hangs for minutes with `screenshot-el`) |
| [goeric, PR #52](https://github.com/simonw/rodney/pull/52) | `920b808` | `rodney start --extension PATH` loads unpacked, `.crx` or `.zip` extensions in headless mode; `rodney extensions` lists their IDs |
| [ejolly/rodney](https://github.com/ejolly/rodney) | `e715d1a` | Safe parallel use: `--session NAME` gives each agent its own Chrome, `--target ID` pins a command to a tab, tabs are tracked by stable ID, state writes are atomic, and `start` reuses a live browser instead of killing it (`--replace` to restart). Also `open --wait/--timeout/--expect-ok/--then-js`, `js --json`, `start --user-agent/--stealth`, `rodney version`, and one-line errors instead of Go stack traces |
| [chrisperfer/rodney](https://github.com/chrisperfer/rodney) | `8b0154c` | `rodney console` to read the page's console output and uncaught errors, `js -` (stdin) and `js --file PATH`, `--page <index or URL/title text>` to target one command at a tab, HTTP cache off by default (`rodney no-cache on\|off`), and `--show` opening maximized |

### Changes made in this fork

- **Headless viewport and User-Agent.** chrisperfer's branch turned off go-rod's built-in 1280x800 device for every session. In a visible window that's right, but in headless mode it meant pages were laid out at a different size, which shifts responsive layouts, and sent a `HeadlessChrome` User-Agent that bot checks look for. Now visible sessions render at the real window size, while headless sessions keep 1280x800 with the browser's real User-Agent minus the "Headless" part (go-rod's default claimed Chrome 114, which some modern sites reject). `start --user-agent` and `--stealth` now actually apply in headless mode too.
- **`--page` was being ignored.** After merging, the saved tab was always checked first. The order is now `--page`, then `--target`, then the saved tab.
- **Console log privacy.** `console.log` captures output from every tab, which can include tokens. It's now readable only by its owner, and the capture process stays attached to the session that started it.
- **`js --json` waits for promises** instead of printing `{}`. Scripts passed with `--file` or stdin can end with `;` or a `//` comment.
- **What pages can tell about the browser is set at launch.** rodney runs one process per command, and Chrome drops every CDP override (User-Agent, viewport) when the command that set it exits. So after `rodney open` returned, a page's later requests and scripts saw the raw headless browser: `HeadlessChrome` in the UA, an 800x600 screen, empty `navigator.userAgentData`, no `Sec-CH-UA` headers. Headless sessions now get the real UA minus "Headless" on Chrome's command line, plus a window whose page area is still 1280x800. Every session hides `navigator.webdriver`. Behind a proxy, a profile preference stops WebRTC from revealing the host's own IP, which it otherwise did even through the proxy. Measured with [deviceandbrowserinfo.com's bot test](https://deviceandbrowserinfo.com/are_you_a_bot): six signals before, none with `--stealth`.
- **`--human`, `--no-console`, `--stealth`.** `--human` makes `click`, `input`, `clear`, `hover` and the new `scroll` act like a person. The pointer follows curved paths sized by Fitts's law, borrowed from [ghost-cursor](https://github.com/Xetera/ghost-cursor), at minimum-jerk speed. Clicks land on a visible point of the element and hold the button for a human interval. Typing goes key by key with a typist's rhythm, and scrolling uses wheel notches. `--no-console` skips the console capture, whose `Runtime.enable` bot checks detect. `--stealth` is both.
- **`--timezone`, `--lang`, `--window`, `--chrome-arg`, `RODNEY_START_FLAGS`** let a crawler host present itself as a desktop in the place its IP says it is. For example, a server relayed through a home connection can run headed Chrome under Xvfb with the home's timezone and languages.
- **Reddit gets no client hints.** From a home IP in October 2026, Reddit answered recent Chrome (146, 154) that sends User-Agent client hints with "Prove your humanity", headless or headed, whatever the rest of the fingerprint. Without the hints it passes Reddit's JS challenge. On reddit.com, redd.it and redditmedia.com, every command sets a User-Agent override with the session's own UA and languages and no client-hint metadata, before navigating for `open` and `newpage`. `RODNEY_NO_CLIENT_HINTS` changes the host list (`none` turns it off).
- **The field-trial testing config is back on** for rod's Chromium snapshot; only HistoryEmbeddings, the feature that crashed, is switched off. With the whole config off, the browser's features matched no released Chrome 128, and Reddit's JS challenge refused it.
- **Tooling.** ejolly's `just`/golangci-lint setup (about 240 extra lines in `go.mod`) is replaced by a `Makefile`. Run `make` for the list of targets. Contributor notes are in [AGENTS.md](AGENTS.md).

### What was left out, and why

- **zbkilla's `rodney network`** (request and response capture). It's a feature I want, but the commits come from an anonymous identity (`Demo User <demo@example.com>`) and the code had real safety problems:
  - a malformed pid file could make `network record stop` kill every process you own;
  - request IDs coming from the browser were used as file paths without checks;
  - captured auth headers were saved world-readable;
  - the capture process never exits.

  Better to write this cleanly than patch it.
- **Large bundle forks** (jamalex, Battle-Creek-LLC, devskale and others). They are big rewrites or roll-ups of the same open PRs, would break existing commands, and have gone quiet.

### Behaviour changes from upstream

- The HTTP cache is off by default. Start with `rodney start --cache` for a normally caching browser. `rodney no-cache off` turns HTTP caching back on for a running session, but the disk cache stays off.
- `rodney start` reuses a browser that's already running, and warns about any launch options it had to ignore. Use `--replace` to restart it.
- On macOS, Chrome no longer runs with `--single-process`.
- `navigator.webdriver` is false and the automation infobar is gone in every session. `--stealth` now means `--human --no-console`. It no longer swaps in a fixed Mac Chrome 131 UA, because every session now gets the real browser's UA.
- `--disable-gpu` is only passed to headless Chrome on Linux. Headless macOS renders WebGL on the real GPU, and headed windows keep theirs.

### Install

```bash
git clone https://github.com/rodrigolive/rodney
cd rodney
make build        # ./rodney, version-stamped from git
make install      # copies it to INSTALL_DIR (default ~/util)
```

Requires Go 1.25+. Chrome/Chromium is downloaded automatically on first `start`, or set `ROD_CHROME_BIN`.

### Status

Upstream's `test.yml` only builds the binary and checks `--version`. This fork adds `ci.yml`, which on every push runs:
- `go vet`, gofmt and a `go mod tidy` check;
- the Go test suite and `test.sh`, both against rod's pinned Chromium, on Linux and macOS.

Check the Actions tab for the current result. `test.sh` now has its fixture server in `tests/testserver`; upstream relied on a `/tmp/testserver` binary that was never committed. `make test-sh` runs it in a throwaway `RODNEY_HOME`, so it never touches your own rodney session.

### Keeping up with upstream

`make upstream` fetches simonw/rodney and lists any commits that aren't merged here yet. If upstream starts merging again, this fork will follow it, and anything here that lands upstream will be dropped.

## Architecture

```
rodney start          →  launches Chrome (headless, persists after CLI exits)
                          saves WebSocket debug URL to ~/.rodney/state.json

rodney connect H:P    →  connects to an existing Chrome on a remote debug port
                          saves WebSocket debug URL to ~/.rodney/state.json

rodney open URL       →  connects to running Chrome via WebSocket
                          navigates the active tab, disconnects

rodney js EXPR        →  connects, evaluates JS, prints result, disconnects

rodney stop           →  connects and shuts down Chrome, cleans up state
```

Each CLI invocation is a short-lived process. Chrome runs independently and tabs persist between commands.

## Building

```bash
go build -o rodney .
```

Requires:
- Go 1.25+
- Google Chrome or Chromium installed (or set `ROD_CHROME_BIN=/path/to/chrome`)

## Usage

### Start/stop the browser

```bash
rodney start              # Launch headless Chrome
rodney start --show       # Launch with visible browser window
rodney start --insecure   # Launch with TLS errors ignored (-k shorthand)
rodney start --stealth    # Hide common headless automation tells
rodney start --user-agent "Mozilla/5.0 (...) Chrome/131.0.0.0 ..."  # Override the User-Agent
rodney connect host:9222  # Connect to existing Chrome on remote debug port
rodney status             # Show browser info and active page (includes version)
rodney version            # Print version
rodney stop               # Shut down Chrome
```

### Browser extensions

`--extension` loads a Chrome extension at startup. It works in headless mode as
well as with `--show`:

```bash
# An unpacked extension directory
rodney start --extension ./my-extension

# A packed .crx or .zip, which is unpacked into the session directory
rodney start --extension ./my-extension.crx

# Repeat the flag to load more than one
rodney start --extension ./one --extension ./two
```

`rodney extensions` lists what was loaded, including the ID Chrome assigned to
each one, which you need to reach pages served by the extension:

```bash
rodney extensions
# ldmakemplfmadpiihagnajidjbhnjlcm  My Extension  1.0.0  /path/to/my-extension

rodney open chrome-extension://ldmakemplfmadpiihagnajidjbhnjlcm/popup.html
rodney screenshot popup.png
```

Notes:

- Extensions run in Chrome's [new headless
  mode](https://developer.chrome.com/docs/chromium/new-headless), which rodney
  switches to automatically when `--extension` is used — the old headless mode
  cannot run extensions at all.
- Chrome only loads *unpacked* extensions from the command line, so a `.crx` is
  unpacked before being loaded. It therefore gets an ID derived from its path on
  disk rather than the ID it would have when installed from the Web Store.
- Only the extensions passed to `--extension` are enabled, and they stay loaded
  for the lifetime of the session.

### Navigate

```bash
rodney open https://example.com    # Navigate to URL (waits for the load event)
rodney open example.com            # http:// prefix added automatically
rodney back                        # Go back
rodney forward                     # Go forward
rodney reload                      # Reload page
rodney reload --hard               # Reload bypassing cache
rodney clear-cache                 # Clear the browser cache
```

`open` waits for the `load` event by default. On pages that never fire it
(SPAs, long-poll, bot-hostile sites), use the wait/timeout options instead of
hanging:

```bash
rodney open https://spa.app --no-wait                # Return immediately; poll the DOM yourself
rodney open https://spa.app --wait domcontentloaded  # Wait for DOMContentLoaded, not full load
rodney open https://slow.app --timeout 5             # Cap the wait at N seconds
rodney open https://api.example --expect-ok          # Exit non-zero on HTTP status >= 400
rodney open https://example.com --then-js 'document.title'  # Navigate, wait, eval, print — one process
rodney open https://example.com --user-agent "Custom UA/1.0"
```

When a page or tab crashes the renderer, commands fail with a one-line error
(and a hint to restart) rather than a Go stack trace. Set `RODNEY_DEBUG=1` to
see the full trace for development.

### Extract information

```bash
rodney url                    # Print current URL
rodney title                  # Print page title
rodney text "h1"              # Print text content of element
rodney html "div.content"     # Print outer HTML of element
rodney html                   # Print full page HTML
rodney attr "a#link" href     # Print attribute value
rodney pdf output.pdf         # Save page as PDF
```

### Run JavaScript

```bash
rodney js document.title                        # Evaluate expression
rodney js "1 + 2"                               # Math
rodney js 'document.querySelector("h1").textContent'  # DOM queries
rodney js '[1,2,3].map(x => x * 2)'            # Returns pretty-printed JSON
rodney js 'document.querySelectorAll("a").length'     # Count elements
rodney js --json 'document.title'              # Always valid JSON (quoted) — pipe to jq/jaq
rodney js --timeout 5 'slowThing()'            # Override the eval timeout
```

The expression is automatically wrapped in `() => { return (expr); }`. By
default strings print unquoted and objects/arrays pretty-print as JSON; pass
`--json` to `JSON.stringify` the result so the output is *always* valid JSON
(handy for piping into `jq`/`jaq`).

### Interact with elements

```bash
rodney click "button#submit"       # Click element
rodney input "#search" "query"     # Type into input field
rodney clear "#search"             # Clear input field
rodney file "#upload" photo.png    # Set file on a file input
rodney file "#upload" -            # Set file from stdin
rodney download "a.pdf-link"       # Download href/src target to file
rodney download "a.pdf-link" -     # Download to stdout
rodney select "#dropdown" "value"  # Select dropdown by value
rodney submit "form#login"         # Submit a form
rodney hover ".menu-item"          # Hover over element
rodney focus "#email"              # Focus element
```

### Wait for conditions

```bash
rodney wait ".loaded"       # Wait for element to appear and be visible
rodney waitload             # Wait for page load event
rodney waitstable           # Wait for DOM to stop changing
rodney waitidle             # Wait for network to be idle
rodney sleep 2.5            # Sleep for N seconds
```

The `open`, `text`, `js`, `wait`, `waitload`, `waitstable`, `waitidle`, and
`reload` commands all accept `--timeout SEC` to override the default 30s
(`ROD_TIMEOUT`) timeout per invocation.

### Screenshots

```bash
rodney screenshot                         # Save as screenshot.png
rodney screenshot page.png                # Save to specific file
rodney screenshot -w 1280 -h 720 out.png  # Set viewport width/height
rodney screenshot-el ".chart" chart.png   # Screenshot specific element
```

### Manage tabs

```bash
rodney pages                    # List all tabs (* marks active; shows target id)
rodney newpage https://...      # Open URL in new tab; prints "target: <id>"
rodney page 1                   # Switch to tab by index
rodney page <target-id>         # ...or by stable target id
rodney closepage 1              # Close tab by index or target id
rodney closepage                # Close active tab
```

Tabs are listed in a stable order (sorted by target id) so an index maps to the
same tab across calls as long as no tab opens or closes. Target ids never shift —
prefer them for reliable multi-step or multi-agent scripts (see
[Running multiple agents in parallel](#running-multiple-agents-in-parallel)).

### Query elements

```bash
rodney exists ".loading"    # Exit 0 if exists, exit 1 if not
rodney count "li.item"      # Print number of matching elements
rodney visible "#modal"     # Exit 0 if visible, exit 1 if not
rodney assert 'document.title' 'Home'  # Exit 0 if equal, exit 1 if not
rodney assert 'document.querySelector("h1") !== null'  # Exit 0 if truthy
```

### Accessibility testing

```bash
rodney ax-tree                           # Dump full accessibility tree
rodney ax-tree --depth 3                 # Limit tree depth
rodney ax-tree --json                    # Output as JSON

rodney ax-find --role button             # Find all buttons
rodney ax-find --name "Submit"           # Find by accessible name
rodney ax-find --role link --name "Home" # Combine filters
rodney ax-find --role button --json      # Output as JSON

rodney ax-node "#submit-btn"             # Inspect element's a11y properties
rodney ax-node "h1" --json               # Output as JSON
```

These commands use Chrome's [Accessibility CDP domain](https://chromedevtools.github.io/devtools-protocol/tot/Accessibility/) to expose what assistive technologies see. `ax-tree` uses `getFullAXTree`, `ax-find` uses `queryAXTree`, and `ax-node` uses `getPartialAXTree`.

```bash
# CI check: verify all buttons have accessible names
rodney ax-find --role button --json | python3 -c "
import json, sys
buttons = json.load(sys.stdin)
unnamed = [b for b in buttons if not b.get('name', {}).get('value')]
if unnamed:
    print(f'FAIL: {len(unnamed)} button(s) missing accessible name')
    sys.exit(1)
print(f'PASS: all {len(buttons)} buttons have accessible names')
"
```

### Directory-scoped sessions

By default, Rodney stores state globally in `~/.rodney/`. You can instead create a session scoped to the current directory with `--local`:

```bash
rodney start --local          # State stored in ./.rodney/state.json
                              # Chrome data in ./.rodney/chrome-data/
rodney open https://example.com   # Auto-detects local session
rodney stop                       # Cleans up local session
```

This is useful when you want isolated browser sessions per project — each directory gets its own Chrome instance, cookies, and state.

**Auto-detection:** When neither `--local` nor `--global` is specified, Rodney checks for `./.rodney/state.json` in the current directory. If found, it uses the local session; otherwise it falls back to the global `~/.rodney/` session.

```bash
# Force global even when a local session exists
rodney --global open https://example.com

# Force local (errors if no local session)
rodney --local status
```

The `--local` and `--global` flags can appear anywhere in the command:

```bash
rodney --local start
rodney start --local          # Same effect
rodney open --global https://example.com
```

Add `.rodney/` to your `.gitignore` to keep session state out of version control.

### Running multiple agents in parallel

Rodney is designed to be driven by several agents at once without their commands
interfering. There are two models; pick based on whether the agents should share
cookies/session.

**Multiple connections are not a `rod` limitation** — every rodney command opens
its own short-lived CDP connection to Chrome, and Chrome happily serves many at
once. The only thing that must not be shared blindly is the *active tab* pointer,
which is why the two patterns below exist.

#### 1. Isolation — one Chrome per agent (simplest, recommended)

Give each agent its own state directory (and therefore its own Chrome instance,
cookies, and tabs) with `RODNEY_SESSION` (or the `--session NAME` flag). Set it
once per agent and use rodney exactly as normal — nothing else changes:

```bash
export RODNEY_SESSION=agent-1   # each agent picks a unique name
rodney start
rodney open https://example.com
rodney text h1
rodney stop
```

State lives under `~/.rodney/sessions/agent-1/`. Agents never share a tab pointer,
so nothing can conflict. The cost is one Chrome process per agent.

#### 2. Shared browser — one Chrome, a tab per agent

When agents should share one browser (and its cookies), have each agent open its
own tab and pin every subsequent command to that tab's **stable target id** with
`RODNEY_TARGET` (or `--target ID`):

```bash
rodney start
id=$(rodney newpage https://example.com | sed -n 's/^target: //p')  # capture the tab id
RODNEY_TARGET=$id rodney open https://example.com/login
RODNEY_TARGET=$id rodney click "#submit"
RODNEY_TARGET=$id rodney text ".result"
```

Target ids are printed by `rodney newpage` (as `target: <id>`) and `rodney pages`,
and stay valid for the life of the tab. Because each agent addresses its own tab
explicitly, two agents can interleave commands freely without clobbering each
other's "current tab".

> Tab indices (`rodney page 1`) are sorted by target id so they stay consistent
> between calls, but they still shift when tabs open or close. For robust
> multi-step or multi-agent work, prefer the target id.

### Shell scripting examples

```bash
# Wait for page to load and extract data
rodney start
rodney open https://example.com
rodney waitstable
title=$(rodney title)
echo "Page: $title"

# Conditional logic based on element presence
if rodney exists ".error-message"; then
    rodney text ".error-message"
fi

# Loop through pages
for url in page1 page2 page3; do
    rodney open "https://example.com/$url"
    rodney waitstable
    rodney screenshot "${url}.png"
done

rodney stop
```

## Exit codes

Rodney uses distinct exit codes to separate check failures from errors:

| Exit code | Meaning |
|---|---|
| `0` | Success |
| `1` | Check failed — the command ran successfully but the condition/assertion was not met |
| `2` | Error — something went wrong (bad arguments, no browser session, timeout, etc.) |

This makes it easy to distinguish between "the assertion is false" and "the command couldn't run" in scripts and CI pipelines.

## Using Rodney for checks

Several commands return **exit code 1** when a condition is not met, making them useful as assertions in shell scripts and CI pipelines. All of these print their result to stdout and exit cleanly — no error message is written to stderr.

### `exists` — check if an element exists in the DOM

```bash
rodney exists "h1"
# Prints "true", exits 0

rodney exists ".nonexistent"
# Prints "false", exits 1
```

### `visible` — check if an element is visible

```bash
rodney visible "#modal"
# Prints "true" and exits 0 if the element exists and is visible

rodney visible "#hidden-div"
# Prints "false" and exits 1 if the element is hidden or doesn't exist
```

### `ax-find` — check for accessibility nodes

```bash
rodney ax-find --role button --name "Submit"
# Prints the matching node(s), exits 0

rodney ax-find --role banner --name "Nonexistent"
# Prints "No matching nodes" to stderr, exits 1
```

### `assert` — assert a JavaScript expression

With one argument, checks that the expression is truthy. With two arguments, checks that the expression's value equals the expected string. Use `--message` / `-m` to set a custom failure message.

```bash
# Truthy mode — check that expression evaluates to a truthy value
rodney assert 'document.querySelector(".logged-in") !== null'
# Prints "pass", exits 0

rodney assert 'document.querySelector(".nonexistent")'
# Prints "fail: got null", exits 1

# Equality mode — check that expression result matches expected value
rodney assert 'document.title' 'Dashboard'
# Prints "pass" if title is "Dashboard", exits 0

rodney assert 'document.querySelectorAll(".item").length' '3'
# Prints "pass" if there are exactly 3 items, exits 0

rodney assert 'document.title' 'Wrong Title'
# Prints 'fail: got "Dashboard", expected "Wrong Title"', exits 1
```

The expression is evaluated the same way as `rodney js` — the result is converted to its string representation before comparison. This means `rodney assert 'document.title' 'Dashboard'` compares the unquoted string, and `rodney assert '1 + 2' '3'` compares the number as a string.

Use `--message` (or `-m`) to add a human-readable description to the failure output:

```bash
rodney assert 'document.querySelector(".logged-in")' -m "User should be logged in"
# On failure: "fail: User should be logged in (got null)"

rodney assert 'document.title' 'Dashboard' --message "Wrong page loaded"
# On failure: 'fail: Wrong page loaded (got "Home", expected "Dashboard")'
```

### Combining checks in a shell script

You can chain these together in a single script to run multiple assertions. Because check failures use exit code 1 while real errors use exit code 2, you can use `set -e` to abort on errors while handling check failures explicitly:

```bash
#!/bin/bash
set -euo pipefail

FAIL=0

check() {
    if ! "$@"; then
        echo "FAIL: $*"
        FAIL=1
    fi
}

rodney start
rodney open "https://example.com"
rodney waitstable

# Assert elements exist
check rodney exists "h1"
check rodney exists "nav"
check rodney exists "footer"

# Assert key elements are visible
check rodney visible "h1"
check rodney visible "#main-content"

# Assert JS expressions
check rodney assert 'document.title' 'Example Domain'
check rodney assert 'document.querySelectorAll("p").length' '2'
check rodney assert 'document.querySelector("h1") !== null'

# Assert accessibility requirements
check rodney ax-find --role navigation
check rodney ax-find --role heading --name "Example Domain"

rodney stop

if [ "$FAIL" -ne 0 ]; then
    echo "Some checks failed"
    exit 1
fi
echo "All checks passed"
```

This pattern is useful in CI — run Rodney as a post-deploy check, an accessibility audit, or a smoke test against a staging environment. Because exit code 2 signals an actual error (e.g. Chrome didn't start), `set -e` will abort the script immediately if something is broken rather than reporting a misleading test failure.

## Configuration

| Environment Variable | Default | Description |
|---|---|---|
| `RODNEY_HOME` | `~/.rodney` | Data directory for state and Chrome profile |
| `RODNEY_SESSION` | (unset) | Isolate state under `sessions/<name>/` — each agent gets its own Chrome |
| `RODNEY_TARGET` | (unset) | Operate on the tab with this target id (shared-browser parallelism) |
| `ROD_CHROME_BIN` | `/usr/bin/google-chrome` | Path to Chrome/Chromium binary |
| `ROD_TIMEOUT` | `30` | Default timeout in seconds for element queries and waits |
| `RODNEY_DEBUG` | (unset) | When set, print full Go stack traces instead of clean errors |
| `HTTPS_PROXY` / `HTTP_PROXY` | (none) | Authenticated proxy auto-detected on start |

Global state is stored in `~/.rodney/state.json` with Chrome user data in `~/.rodney/chrome-data/`. When using `--local`, state is stored in `./.rodney/state.json` and `./.rodney/chrome-data/` in the current directory instead. Set `RODNEY_HOME` to override the default global directory.

## Proxy support

In environments with authenticated HTTP proxies (e.g., `HTTPS_PROXY=http://user:pass@host:port`), `rodney start` automatically:

1. Detects the proxy credentials from environment variables
2. Launches a local forwarding proxy that injects `Proxy-Authorization` headers into CONNECT requests
3. Configures Chrome to use the local proxy

This is necessary because Chrome cannot natively authenticate to proxies during HTTPS tunnel (CONNECT) establishment. The local proxy runs as a background process and is automatically cleaned up by `rodney stop`.

See [claude-code-chrome-proxy.md](claude-code-chrome-proxy.md) for detailed technical notes.

## How it works

The tool uses the [rod](https://github.com/go-rod/rod) Go library which communicates with Chrome via the DevTools Protocol (CDP) over WebSocket. Key implementation details:

- **`start`** uses rod's `launcher` package to start Chrome with `Leakless(false)` so Chrome survives after the CLI exits
- **Proxy auth** handled via a local forwarding proxy that bridges Chrome to authenticated upstream proxies
- **State persistence** via a JSON file containing the WebSocket debug URL and Chrome PID
- **Each command** creates a new rod `Browser` connection to the same Chrome instance, executes the operation, and disconnects
- **Element queries** use rod's built-in auto-wait with a configurable timeout (default 30s)
- **JS evaluation** wraps user expressions in arrow functions as required by rod's `Eval`
- **Accessibility commands** call CDP's Accessibility domain directly via rod's `proto` package (`getFullAXTree`, `queryAXTree`, `getPartialAXTree`)
- **Graceful failure** — a top-level `recover()` turns any internal panic into a clean `error: …` + exit 2 (`RODNEY_DEBUG=1` restores the full trace), and navigation commands apply the timeout and add a restart hint when the browser session has died

## Dependencies

- [github.com/go-rod/rod](https://github.com/go-rod/rod) v0.116.2 - Chrome DevTools Protocol automation

## Commands reference

| Command | Arguments | Description |
|---|---|---|
| `start` | `[--show] [--insecure\|-k] [--user-agent UA] [--stealth] [--replace]` | Launch Chrome (headless by default; reuses a running session unless `--replace`) |
| `connect` | `<host:port>` | Connect to existing Chrome on remote debug port |
| `stop` | | Shut down Chrome |
| `status` | | Show browser status (includes version) |
| `version` | | Print version |
| `open` | `<url> [--no-wait] [--wait MODE] [--timeout SEC] [--expect-ok] [--then-js EXPR] [--user-agent UA]` | Navigate to URL (`MODE`: `load`\|`domcontentloaded`\|`none`) |
| `back` | | Go back in history |
| `forward` | | Go forward in history |
| `reload` | `[--hard] [--timeout SEC]` | Reload page (`--hard` bypasses cache) |
| `clear-cache` | | Clear the browser cache |
| `url` | | Print current URL |
| `title` | | Print page title |
| `html` | `[selector]` | Print HTML (page or element) |
| `text` | `<selector> [--timeout SEC]` | Print element text content |
| `attr` | `<selector> <name>` | Print attribute value |
| `pdf` | `[file]` | Save page as PDF |
| `js` | `[--timeout SEC] [--json] <expression>` | Evaluate JavaScript (`--json` for always-valid JSON) |
| `click` | `<selector>` | Click element |
| `input` | `<selector> <text>` | Type into input |
| `clear` | `<selector>` | Clear input |
| `file` | `<selector> <path\|->` | Set file on a file input (`-` for stdin) |
| `download` | `<selector> [file\|-]` | Download href/src target (`-` for stdout) |
| `select` | `<selector> <value>` | Select dropdown value |
| `submit` | `<selector>` | Submit form |
| `hover` | `<selector>` | Hover over element |
| `focus` | `<selector>` | Focus element |
| `wait` | `<selector> [--timeout SEC]` | Wait for element to appear |
| `waitload` | `[--timeout SEC]` | Wait for page load |
| `waitstable` | `[--timeout SEC]` | Wait for DOM stability |
| `waitidle` | `[--timeout SEC]` | Wait for network idle |
| `sleep` | `<seconds>` | Sleep N seconds |
| `screenshot` | `[-w N] [-h N] [file]` | Page screenshot (optional viewport size) |
| `screenshot-el` | `<selector> [file]` | Element screenshot |
| `pages` | | List tabs (with target ids) |
| `page` | `<index\|target-id>` | Switch tab by index or target id |
| `newpage` | `[url]` | Open new tab (prints its target id) |
| `closepage` | `[index\|target-id]` | Close a tab (defaults to active) |
| `exists` | `<selector>` | Check element exists (exit 1 if not) |
| `count` | `<selector>` | Count matching elements |
| `visible` | `<selector>` | Check element visible (exit 1 if not) |
| `assert` | `<expr> [expected] [-m msg]` | Assert JS expression is truthy or equals expected (exit 1 if not) |
| `ax-tree` | `[--depth N] [--json]` | Dump accessibility tree |
| `ax-find` | `[--name N] [--role R] [--json]` | Find accessible nodes |
| `ax-node` | `<selector> [--json]` | Show element accessibility info |

### Global flags

| Flag | Description |
|---|---|
| `--local` | Use directory-scoped session (`./.rodney/`) |
| `--global` | Use global session (`~/.rodney/`) |
| `--session NAME` | Isolate state under `sessions/NAME/` — own Chrome (per-agent) |
| `--target ID` | Operate on the tab with this target id (shared-browser parallelism) |
| `--version` | Print version and exit |
| `--help`, `-h`, `help` | Show help message |

All global flags may appear anywhere in the command (e.g. `rodney open --session a1 example.com`).
