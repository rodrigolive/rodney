# CLAUDE.md

@AGENTS.md

## Picking commits from other forks

This fork merges reviewed work from other simonw/rodney forks. To pull more:

1. Add the fork as a remote: `git remote add <owner> https://github.com/<owner>/rodney && git fetch <owner>`.
2. List only what's new since the last review: `git log --oneline <reviewed-sha>..<owner>/<branch>`.
3. Treat anything after the reviewed SHA as unreviewed. Read every added line and check for:
   - network calls beyond the local CDP endpoint, and exec;
   - file writes or deletes outside the state dir;
   - new dependencies (check go.mod against proxy.golang.org);
   - CI workflows that use secrets or publish;
   - text aimed at AI agents;
   - who authored each commit.
4. Merge or cherry-pick by SHA, never by branch name. Explain the conflict resolution in the commit message.
5. Update the README's "What was merged" table and the table below.

| Fork / branch | Reviewed up to | Result |
|---|---|---|
| goeric `fix/chromium-browser-process-crashes` (PR #54) | `663fc04` | merged |
| goeric `fix/activate-target-before-capture` (PR #55) | `62b5bbf` | merged |
| goeric `load-extensions` (PR #52) | `920b808` | merged |
| ejolly `main` | `e715d1a` | merged; just/golangci tooling stripped afterwards |
| chrisperfer `main` | `8b0154c` | merged |
| zbkilla `main` | `91e9258` | rejected: anonymous commit identity, pid-file kill with no `pid > 0` check, CDP request IDs used as file paths. Reimplement `rodney network` rather than merge it |

Not reviewed yet, but possibly worth a look: johnwards `viewport-command` (upstream PR #40), simbo1905 cookie commands (PR #47), stephenheron `ROD_CHROME_ARGS` (PR #8), layneson `mouse` subcommand. `make upstream` lists simonw/rodney commits not merged here.
