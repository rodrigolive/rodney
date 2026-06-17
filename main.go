package main

import (
	"cmp"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

//go:embed help.txt
var helpText string

var version = "dev"

// scopeMode determines whether to use a local or global state directory.
type scopeMode int

const (
	scopeAuto   scopeMode = iota // auto-detect: local if .rodney/state.json exists in cwd, else global
	scopeLocal                   // force local (./.rodney/)
	scopeGlobal                  // force global (~/.rodney/)
)

// activeStateDir is set once at startup based on --local/--global flags.
var activeStateDir string

// activeSession is set from --session NAME or RODNEY_SESSION. When non-empty it
// isolates state under <base>/sessions/<name>, giving each agent its own Chrome
// instance (the recommended pattern for running many agents in parallel).
var activeSession string

// activeTarget is set from --target ID or RODNEY_TARGET. When non-empty every
// command operates on the tab with that target id instead of the shared
// "active page", letting multiple agents share one Chrome without clobbering
// each other's current tab.
var activeTarget string

// extractValueFlag removes "--name value" or "--name=value" from args and
// returns the value (last occurrence wins) plus the remaining args. Used for the
// global --session/--target flags, which may appear anywhere in the command.
func extractValueFlag(args []string, name string) (string, []string) {
	value := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == name:
			if i+1 < len(args) {
				value = args[i+1]
				i++
			}
		case strings.HasPrefix(a, name+"="):
			value = a[len(name)+1:]
		default:
			rest = append(rest, a)
		}
	}
	return value, rest
}

// sanitizeSession keeps a session name from escaping the sessions/ directory.
func sanitizeSession(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "..", "_")
	return name
}

// extractScopeArgs scans args for --local/--global, removes them, and returns the mode.
// If both appear, the last one wins.
func extractScopeArgs(args []string) (scopeMode, []string) {
	mode := scopeAuto
	var filtered []string
	for _, arg := range args {
		switch arg {
		case "--local":
			mode = scopeLocal
		case "--global":
			mode = scopeGlobal
		default:
			filtered = append(filtered, arg)
		}
	}
	return mode, filtered
}

// resolveStateDir determines the state directory based on scope mode and working directory.
func resolveStateDir(mode scopeMode, workingDir string) string {
	switch mode {
	case scopeLocal:
		return filepath.Join(workingDir, ".rodney")
	case scopeGlobal:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	default: // scopeAuto
		localDir := filepath.Join(workingDir, ".rodney")
		if _, err := os.Stat(filepath.Join(localDir, "state.json")); err == nil {
			return localDir
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	}
}

// State persisted between CLI invocations
type State struct {
	DebugURL     string `json:"debug_url"`
	ChromePID    int    `json:"chrome_pid"`
	ActivePage   int    `json:"active_page"`             // legacy: index into the (stably sorted) pages list
	ActiveTarget string `json:"active_target,omitempty"` // stable target id of the active tab
	DataDir      string `json:"data_dir"`
	ProxyPID     int    `json:"proxy_pid,omitempty"`  // PID of auth proxy helper
	ProxyPort    int    `json:"proxy_port,omitempty"` // local port of auth proxy
}

// baseStateDir resolves the data directory before any --session scoping.
func baseStateDir() string {
	if dir := os.Getenv("RODNEY_HOME"); dir != "" {
		return dir
	}
	if activeStateDir != "" {
		return activeStateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

func stateDir() string {
	base := baseStateDir()
	if activeSession != "" {
		return filepath.Join(base, "sessions", sanitizeSession(activeSession))
	}
	return base
}

func statePath() string {
	return filepath.Join(stateDir(), "state.json")
}

func loadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("no browser session (run 'rodney start' first)")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupt state file: %w", err)
	}
	return &s, nil
}

// saveState writes state.json atomically (temp file + rename) so a concurrent
// reader or a second agent writing at the same time can never observe a
// half-written, corrupt file.
func saveState(s *State) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "state-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, statePath()); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

func removeState() {
	_ = os.Remove(statePath())
}

// connectBrowser connects to the running Chrome instance
func connectBrowser(s *State) (*rod.Browser, error) {
	browser := rod.New().ControlURL(s.DebugURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to browser (is it still running?): %w", err)
	}
	return browser, nil
}

// orderedPages returns the browser's pages in a stable order (sorted by target
// id). Chrome's own target ordering is not guaranteed — newly opened tabs can
// appear first — so without this a positional index like "page 1" would point
// at different tabs across invocations. Sorting by the immutable target id makes
// index N map to the same tab as long as the set of tabs is unchanged.
func orderedPages(browser *rod.Browser) (rod.Pages, error) {
	pages, err := browser.Pages()
	if err != nil {
		return nil, fmt.Errorf("failed to list pages: %w", err)
	}
	slices.SortFunc(pages, func(a, b *rod.Page) int {
		return cmp.Compare(string(a.TargetID), string(b.TargetID))
	})
	return pages, nil
}

// matchTargetID resolves a target-id query against the available ids: an exact
// match wins, otherwise a unique prefix match. The errors are written for an
// agent to act on (no match / ambiguous), not a stack trace.
func matchTargetID(ids []string, query string) (int, error) {
	for i, id := range ids {
		if id == query {
			return i, nil
		}
	}
	match := -1
	for i, id := range ids {
		if strings.HasPrefix(id, query) {
			if match >= 0 {
				return -1, fmt.Errorf("target id %q is ambiguous; use the full id from 'rodney pages'", query)
			}
			match = i
		}
	}
	if match < 0 {
		return -1, fmt.Errorf("no tab with target id %q (run 'rodney pages' to list open tabs)", query)
	}
	return match, nil
}

// targetIDs returns the target ids of pages in order.
func targetIDs(pages rod.Pages) []string {
	ids := make([]string, len(pages))
	for i, p := range pages {
		ids[i] = string(p.TargetID)
	}
	return ids
}

// pageByTarget finds the page whose target id matches query (exact or unique prefix).
func pageByTarget(pages rod.Pages, query string) (*rod.Page, error) {
	idx, err := matchTargetID(targetIDs(pages), query)
	if err != nil {
		return nil, err
	}
	return pages[idx], nil
}

// setActivePage records both the stable target id and the legacy index for the
// chosen tab, so the active tab survives later tabs opening or closing.
func setActivePage(s *State, pages rod.Pages, idx int) {
	s.ActivePage = idx
	if idx >= 0 && idx < len(pages) {
		s.ActiveTarget = string(pages[idx].TargetID)
	}
}

// getActivePage returns the page a command should act on. Resolution order:
//  1. an explicit per-invocation --target / RODNEY_TARGET override,
//  2. the stable active target id stored in state (survives reordering),
//  3. the legacy active-page index, clamped into range.
func getActivePage(browser *rod.Browser, s *State) (*rod.Page, error) {
	pages, err := orderedPages(browser)
	if err != nil {
		return nil, err
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages open")
	}
	if activeTarget != "" {
		return pageByTarget(pages, activeTarget)
	}
	if s.ActiveTarget != "" {
		if p, err := pageByTarget(pages, s.ActiveTarget); err == nil {
			return p, nil
		}
		// The stored tab was closed; fall back to the index.
	}
	idx := s.ActivePage
	if idx < 0 || idx >= len(pages) {
		idx = 0
	}
	return pages[idx], nil
}

func printUsage() {
	fmt.Print(helpText)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(2)
}

// findUnknownFlag returns the first arg not registered in fs, preserving original form (e.g. --bogus).
func findUnknownFlag(args []string, fs *flag.FlagSet) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name := strings.TrimLeft(a, "-")
		if fs.Lookup(name) == nil {
			return a
		}
	}
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func main() {
	// Convert any Must*-style panic into a clean `error: …` + exit 2 so agents
	// scripting rodney never get a raw Go stack trace. RODNEY_DEBUG=1 skips the
	// guard so the full trace is preserved for development.
	if os.Getenv("RODNEY_DEBUG") == "" {
		defer func() {
			if r := recover(); r != nil {
				fatal("%v", r)
			}
		}()
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	// Extract the global flags (--local/--global, --session, --target) from all
	// args before dispatching, so they can appear anywhere in the command.
	mode, cleanedArgs := extractScopeArgs(os.Args[1:])
	var sessionVal, targetVal string
	sessionVal, cleanedArgs = extractValueFlag(cleanedArgs, "--session")
	targetVal, cleanedArgs = extractValueFlag(cleanedArgs, "--target")
	activeSession = cmp.Or(sessionVal, os.Getenv("RODNEY_SESSION"))
	activeTarget = cmp.Or(targetVal, os.Getenv("RODNEY_TARGET"))

	if len(cleanedArgs) == 0 {
		printUsage()
		os.Exit(1)
	}

	wd, _ := os.Getwd()
	activeStateDir = resolveStateDir(mode, wd)

	cmd := cleanedArgs[0]
	args := cleanedArgs[1:]

	if cmd == "--version" {
		fmt.Println(version)
		os.Exit(0)
	}

	switch cmd {
	case "_proxy":
		cmdInternalProxy(args) // hidden: runs the auth proxy helper
	case "start":
		cmdStart(args)
	case "connect":
		cmdConnect(args)
	case "stop":
		cmdStop(args)
	case "status":
		cmdStatus(args)
	case "version":
		fmt.Println(version)
	case "open":
		cmdOpen(args)
	case "back":
		cmdBack(args)
	case "forward":
		cmdForward(args)
	case "reload":
		cmdReload(args)
	case "clear-cache":
		cmdClearCache(args)
	case "url":
		cmdURL(args)
	case "title":
		cmdTitle(args)
	case "html":
		cmdHTML(args)
	case "text":
		cmdText(args)
	case "attr":
		cmdAttr(args)
	case "pdf":
		cmdPDF(args)
	case "js":
		cmdJS(args)
	case "click":
		cmdClick(args)
	case "input":
		cmdInput(args)
	case "clear":
		cmdClear(args)
	case "select":
		cmdSelect(args)
	case "submit":
		cmdSubmit(args)
	case "hover":
		cmdHover(args)
	case "file":
		cmdFile(args)
	case "download":
		cmdDownload(args)
	case "focus":
		cmdFocus(args)
	case "wait":
		cmdWait(args)
	case "waitload":
		cmdWaitLoad(args)
	case "waitstable":
		cmdWaitStable(args)
	case "waitidle":
		cmdWaitIdle(args)
	case "sleep":
		cmdSleep(args)
	case "screenshot":
		cmdScreenshot(args)
	case "screenshot-el":
		cmdScreenshotEl(args)
	case "pages":
		cmdPages(args)
	case "page":
		cmdPage(args)
	case "newpage":
		cmdNewPage(args)
	case "closepage":
		cmdClosePage(args)
	case "exists":
		cmdExists(args)
	case "count":
		cmdCount(args)
	case "visible":
		cmdVisible(args)
	case "assert":
		cmdAssert(args)
	case "ax-tree":
		cmdAXTree(args)
	case "ax-find":
		cmdAXFind(args)
	case "ax-node":
		cmdAXNode(args)
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		if suggestion := suggestCommand(cmd, commandNames); suggestion != "" {
			fmt.Fprintf(os.Stderr, "did you mean '%s'? run 'rodney help' for all commands\n", suggestion)
		} else {
			fmt.Fprintln(os.Stderr, "run 'rodney help' for the list of commands")
		}
		os.Exit(2)
	}
}

// commandNames lists every dispatchable command, used to suggest a correction
// when an agent mistypes one.
var commandNames = []string{
	"start", "connect", "stop", "status", "version",
	"open", "back", "forward", "reload", "clear-cache",
	"url", "title", "html", "text", "attr", "pdf",
	"js", "click", "input", "clear", "select", "submit", "hover", "file", "download", "focus",
	"wait", "waitload", "waitstable", "waitidle", "sleep",
	"screenshot", "screenshot-el",
	"pages", "page", "newpage", "closepage",
	"exists", "count", "visible", "assert",
	"ax-tree", "ax-find", "ax-node",
	"help",
}

// suggestCommand returns the closest command name within a small edit distance,
// or "" when the input is too far from anything to be a useful suggestion.
func suggestCommand(input string, commands []string) string {
	best, bestDist := "", -1
	for _, c := range commands {
		d := levenshtein(input, c)
		if bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	if bestDist >= 0 && bestDist <= 2 {
		return best
	}
	return ""
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// Default timeout for element queries (seconds)
var defaultTimeout = 30 * time.Second

func init() {
	if t := os.Getenv("ROD_TIMEOUT"); t != "" {
		if secs, err := strconv.ParseFloat(t, 64); err == nil {
			defaultTimeout = time.Duration(secs * float64(time.Second))
		}
	}
}

// withPage loads state, connects, and returns the active page with the default
// timeout applied. Caller should NOT close the browser (we just disconnect).
func withPage() (*State, *rod.Browser, *rod.Page) {
	return withPageTimeout(defaultTimeout)
}

// withPageTimeout is like withPage but applies a caller-chosen timeout so
// element queries and waits don't hang forever.
func withPageTimeout(timeout time.Duration) (*State, *rod.Browser, *rod.Page) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}
	return s, browser, page.Timeout(timeout)
}

// parseFlagsInterspersed parses fs against args where flags and positional
// arguments may appear in any order (a plain flag.FlagSet stops at the first
// non-flag). Returns the positional args in their original order.
func parseFlagsInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positionals []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
	return positionals, nil
}

// resolveTimeout returns the page timeout to use: the --timeout flag value (in
// seconds) when positive, otherwise the global defaultTimeout.
func resolveTimeout(flagSecs float64) time.Duration {
	if flagSecs > 0 {
		return time.Duration(flagSecs * float64(time.Second))
	}
	return defaultTimeout
}

// parseTimeoutArgs extracts an optional --timeout <sec> flag (which may appear
// anywhere among args) and returns the resolved page timeout plus the remaining
// positional args. On an unknown flag it prints usage and exits.
func parseTimeoutArgs(name string, args []string) (time.Duration, []string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	secs := fs.Float64("timeout", 0, "")
	positionals, err := parseFlagsInterspersed(fs, args)
	if err != nil {
		fatal("unknown flag: %s", findUnknownFlag(args, fs))
	}
	return resolveTimeout(*secs), positionals
}

// navHint returns an actionable suffix when err looks like the browser
// connection died (e.g. a crashed tab took the whole session down), else "".
func navHint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	for _, sig := range []string{"EOF", "use of closed", "connection refused", "websocket"} {
		if strings.Contains(msg, sig) {
			return "; the browser may have crashed — run 'rodney start' to restart it"
		}
	}
	return ""
}

// waitPageReady blocks until the page reaches the given readiness, honoring the
// page's timeout. mode is "load" (default), "domcontentloaded", or "none".
func waitPageReady(page *rod.Page, mode string) error {
	switch mode {
	case "none":
		return nil
	case "", "load":
		return page.WaitLoad()
	case "domcontentloaded":
		_, err := page.Evaluate(rod.Eval(`() => new Promise(resolve => {
			if (document.readyState !== 'loading') { resolve(true); return; }
			document.addEventListener('DOMContentLoaded', () => resolve(true), { once: true });
		})`).ByPromise())
		return err
	default:
		return fmt.Errorf("invalid wait mode %q (want load, domcontentloaded, or none)", mode)
	}
}

// navigateCapturingStatus navigates to url and returns the HTTP status of the
// main document response (0 if it could not be observed within the page timeout).
func navigateCapturingStatus(page *rod.Page, url string) (int, error) {
	_ = proto.NetworkEnable{}.Call(page)
	var status int
	wait := page.EachEvent(func(e *proto.NetworkResponseReceived) bool {
		if e.Type == proto.NetworkResourceTypeDocument && e.Response != nil {
			status = e.Response.Status
			return true
		}
		return false
	})
	if err := page.Navigate(url); err != nil {
		return 0, err
	}
	wait()
	return status, nil
}

// formatEvalValue renders a JS eval result: strings unquoted, objects/arrays
// pretty-printed as JSON, everything else as its JSON form.
func formatEvalValue(v gson.JSON) string {
	raw := v.JSON("", "")
	switch {
	case raw == "null" || raw == "undefined":
		return raw
	case raw == "true" || raw == "false":
		return raw
	case len(raw) > 0 && raw[0] == '"':
		return v.Str()
	case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
		return v.JSON("", "  ")
	default:
		return raw
	}
}

// evalJSExpr evaluates a bare JS expression on the page and returns its printable
// form. When asJSON is true the value is JSON.stringify'd and pretty-printed so
// the output is always valid JSON (ideal for piping to jq/jaq).
func evalJSExpr(page *rod.Page, expr string, asJSON bool) (string, error) {
	if asJSON {
		result, err := page.Eval(fmt.Sprintf(`() => JSON.stringify((%s) ?? null)`, expr))
		if err != nil {
			return "", err
		}
		var parsed any
		if err := json.Unmarshal([]byte(result.Value.Str()), &parsed); err != nil {
			return "", fmt.Errorf("result is not JSON-serializable: %w", err)
		}
		out, err := json.MarshalIndent(parsed, "", "  ")
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	result, err := page.Eval(fmt.Sprintf(`() => { return (%s); }`, expr))
	if err != nil {
		return "", err
	}
	return formatEvalValue(result.Value), nil
}

// applyUserAgent overrides the page's User-Agent for subsequent requests.
func applyUserAgent(page *rod.Page, ua string) error {
	return page.SetUserAgent(&proto.NetworkSetUserAgentOverride{UserAgent: ua})
}

// stealthUserAgent is a realistic desktop Chrome UA used by --stealth when no
// explicit --user-agent is given, so the headless give-away ("HeadlessChrome")
// is removed from requests.
const stealthUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// applyStealthFlags sets launcher flags that hide the most common headless
// automation tells (navigator.webdriver via AutomationControlled, and the
// automation banner). Lightweight, launch-only — not a full anti-bot bypass.
func applyStealthFlags(l *launcher.Launcher) {
	l.Set("disable-blink-features", "AutomationControlled")
	l.Delete("enable-automation")
}

// --- Commands ---

const startUsage = "usage: rodney start [--show] [--insecure] [--user-agent UA] [--stealth] [--replace]"

// startOpts holds the parsed flags for the "start" command.
type startOpts struct {
	insecure  bool
	headless  bool
	userAgent string
	stealth   bool
	replace   bool
}

// parseStartArgs parses the flags for the "start" command.
func parseStartArgs(args []string) (startOpts, error) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o startOpts
	fs.BoolVar(&o.insecure, "insecure", false, "")
	fs.BoolVar(&o.insecure, "k", false, "")
	fs.StringVar(&o.userAgent, "user-agent", "", "")
	fs.BoolVar(&o.stealth, "stealth", false, "")
	fs.BoolVar(&o.replace, "replace", false, "")
	fs.BoolVar(&o.replace, "force", false, "")
	show := fs.Bool("show", false, "")

	if parseErr := fs.Parse(args); parseErr != nil {
		return startOpts{headless: true}, fmt.Errorf("unknown flag: %s\n%s", findUnknownFlag(args, fs), startUsage)
	}
	if fs.NArg() > 0 {
		return startOpts{headless: true}, fmt.Errorf("unknown flag: %s\n%s", fs.Arg(0), startUsage)
	}
	o.headless = !*show
	return o, nil
}

func cmdStart(args []string) {
	opts, err := parseStartArgs(args)
	if err != nil {
		fatal("%s", err)
	}
	ignoreCertErrors, headless := opts.insecure, opts.headless

	// If a session already exists, default to reusing it rather than killing it —
	// re-running `start` must never silently nuke a live browser (and any tabs a
	// peer agent is using). --replace forces a fresh browser.
	if s, err := loadState(); err == nil {
		if b, err := connectBrowser(s); err == nil {
			if !opts.replace {
				// Reuse: just disconnect (do NOT Close — that would kill Chrome).
				fmt.Printf("Chrome already running (PID %d)\n", s.ChromePID)
				fmt.Printf("Debug URL: %s\n", s.DebugURL)
				fmt.Println("Reusing the existing session (use 'rodney start --replace' to restart it)")
				return
			}
			b.MustClose()
			removeState()
		}
		// Either we are replacing, or the old browser is dead; clean up any
		// stale auth-proxy helper before launching a new one.
		if s.ProxyPID > 0 {
			if proc, perr := os.FindProcess(s.ProxyPID); perr == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
	}

	dataDir := filepath.Join(stateDir(), "chrome-data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fatal("failed to create data dir: %v", err)
	}

	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process"). // Required for screenshots in gVisor/container environments
		Leakless(false).       // Keep Chrome alive after CLI exits
		UserDataDir(dataDir).
		Headless(headless)

	// When in non-headless mode, make sure that we show the startup window immediately
	// (instead of showing a window only after calling "rodney open")
	if !headless {
		l = l.Delete("no-startup-window")
	}

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	// Stealth + user-agent: --stealth hides the headless automation tells and,
	// absent an explicit --user-agent, swaps in a non-headless UA.
	ua := opts.userAgent
	if opts.stealth {
		applyStealthFlags(l)
		if ua == "" {
			ua = stealthUserAgent
		}
	}
	if ua != "" {
		l.Set("user-agent", ua)
	}

	// Detect authenticated proxy and launch helper if needed
	var proxyPID, proxyPort int
	if server, user, pass, needed := detectProxy(); needed {
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

		// Find a free port for the local proxy
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("failed to find free port for proxy: %v", err)
		}
		proxyPort = ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()

		// Launch ourselves as the proxy helper in the background
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "_proxy",
			strconv.Itoa(proxyPort), server, authHeader)
		setSysProcAttr(cmd)
		if err := cmd.Start(); err != nil {
			fatal("failed to start proxy helper: %v", err)
		}
		proxyPID = cmd.Process.Pid
		// Detach so it survives after we exit
		_ = cmd.Process.Release()

		// Wait for the proxy to be ready
		time.Sleep(500 * time.Millisecond)

		l.Set("proxy-server", fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
		ignoreCertErrors = true // Proxy requires ignoring cert errors
		fmt.Printf("Auth proxy started (PID %d, port %d) -> %s\n", proxyPID, proxyPort, server)
	}

	if ignoreCertErrors {
		l.Set("ignore-certificate-errors")
	}

	debugURL := l.MustLaunch()

	// Get Chrome PID from the launcher
	pid := l.PID()

	state := &State{
		DebugURL:   debugURL,
		ChromePID:  pid,
		ActivePage: 0,
		DataDir:    dataDir,
		ProxyPID:   proxyPID,
		ProxyPort:  proxyPort,
	}

	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Printf("Chrome started (PID %d)\n", pid)
	fmt.Printf("Debug URL: %s\n", debugURL)
}

func cmdConnect(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney connect <host:port>")
	}
	hostport := args[0]
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		fatal("argument must be host:port (e.g. localhost:9222): %s", hostport)
	}

	// Fetch the WebSocket debugger URL from Chrome's /json/version endpoint
	resp, err := http.Get("http://" + hostport + "/json/version")
	if err != nil {
		fatal("could not reach browser at %s: %v", hostport, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("failed to read response: %v", err)
	}
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.WebSocketDebuggerURL == "" {
		fatal("unexpected response from browser at %s", hostport)
	}

	// Verify the connection works
	browser := rod.New().ControlURL(info.WebSocketDebuggerURL)
	if err := browser.Connect(); err != nil {
		fatal("could not connect to browser: %v", err)
	}

	// ChromePID=0 signals that we don't own this browser (stop won't kill it)
	state := &State{
		DebugURL:   info.WebSocketDebuggerURL,
		ChromePID:  0,
		ActivePage: 0,
	}
	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Printf("Connected to browser at %s\n", hostport)
	fmt.Printf("Debug URL: %s\n", info.WebSocketDebuggerURL)
}

func cmdStop(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		// Try to kill by PID only if we launched the browser
		if s.ChromePID > 0 {
			proc, err := os.FindProcess(s.ChromePID)
			if err == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
	} else if s.ChromePID > 0 {
		// Only close (and kill) the browser if we launched it
		browser.MustClose()
	}
	// If ChromePID==0 we connected to an external browser; just clear state without closing it
	// Also kill the proxy helper if running
	if s.ProxyPID > 0 {
		if proc, err := os.FindProcess(s.ProxyPID); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
		}
	}
	removeState()
	fmt.Println("Chrome stopped")
}

func cmdStatus(args []string) {
	fmt.Printf("rodney %s\n", version)
	s, err := loadState()
	if err != nil {
		fmt.Println("No active browser session")
		return
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fmt.Printf("Browser not responding (PID %d, state may be stale)\n", s.ChromePID)
		return
	}
	pages, _ := browser.Pages()
	fmt.Printf("Browser running (PID %d)\n", s.ChromePID)
	fmt.Printf("State dir: %s\n", stateDir())
	if activeSession != "" {
		fmt.Printf("Session: %s\n", activeSession)
	}
	fmt.Printf("Debug URL: %s\n", s.DebugURL)
	fmt.Printf("Pages: %d\n", len(pages))
	if page, err := getActivePage(browser, s); err == nil {
		fmt.Printf("Active target: %s\n", page.TargetID)
		info, _ := page.Info()
		if info != nil {
			fmt.Printf("Current: %s - %s\n", info.Title, info.URL)
		}
	}
}

const openUsage = "usage: rodney open <url> [--no-wait] [--wait load|domcontentloaded|none] [--timeout SEC] [--expect-ok] [--then-js EXPR] [--user-agent UA]"

func cmdOpen(args []string) {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noWait := fs.Bool("no-wait", false, "")
	waitMode := fs.String("wait", "load", "")
	timeoutSecs := fs.Float64("timeout", 0, "")
	expectOK := fs.Bool("expect-ok", false, "")
	thenJS := fs.String("then-js", "", "")
	userAgent := fs.String("user-agent", "", "")

	positionals, err := parseFlagsInterspersed(fs, args)
	if err != nil {
		fatal("unknown flag: %s\n%s", findUnknownFlag(args, fs), openUsage)
	}
	if len(positionals) < 1 {
		fatal("%s", openUsage)
	}

	mode := *waitMode
	if *noWait {
		mode = "none"
	}
	switch mode {
	case "none", "load", "domcontentloaded":
	default:
		fatal("invalid --wait %q (want load, domcontentloaded, or none)", mode)
	}
	timeout := resolveTimeout(*timeoutSecs)

	url := positionals[0]
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	// Ensure there's a page to navigate, creating a blank one if none exist, so
	// the navigation path (and status capture) is uniform.
	pages, _ := browser.Pages()
	var page *rod.Page
	if len(pages) == 0 {
		page, err = browser.Page(proto.TargetCreateTarget{})
		if err != nil {
			fatal("failed to open page: %v%s", err, navHint(err))
		}
		// Record the new tab as active by its stable target id (unless the caller
		// pinned a specific --target, which getActivePage would honor instead).
		if activeTarget == "" {
			s.ActivePage = 0
			s.ActiveTarget = string(page.TargetID)
			_ = saveState(s)
		}
	} else {
		page, err = getActivePage(browser, s)
		if err != nil {
			fatal("%v", err)
		}
	}
	page = page.Timeout(timeout)

	if *userAgent != "" {
		if err := applyUserAgent(page, *userAgent); err != nil {
			fatal("failed to set user agent: %v", err)
		}
	}

	// Capture the document status only when --expect-ok asks for it (it adds a
	// NetworkResponseReceived round-trip we don't want on the common path).
	var status int
	if *expectOK {
		status, err = navigateCapturingStatus(page, url)
	} else {
		err = page.Navigate(url)
	}
	if err != nil {
		fatal("navigation failed: %v%s", err, navHint(err))
	}

	if err := waitPageReady(page, mode); err != nil {
		fatal("page did not finish loading within %s: %v%s", timeout, err, navHint(err))
	}

	if *expectOK {
		if status >= 400 {
			fatal("HTTP %d response from %s", status, url)
		}
		if status > 0 {
			fmt.Fprintf(os.Stderr, "HTTP %d\n", status)
		}
	}

	// --then-js folds navigate + extract into one process (cheaper for bulk
	// lookups); otherwise print the page title as before.
	if *thenJS != "" {
		out, err := evalJSExpr(page, *thenJS, false)
		if err != nil {
			fatal("JS error: %v%s", err, navHint(err))
		}
		fmt.Println(out)
		return
	}

	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.Title)
	}
}

func cmdBack(args []string) {
	_, _, page := withPage()
	if err := page.NavigateBack(); err != nil {
		fatal("back failed: %v%s", err, navHint(err))
	}
	if err := page.WaitLoad(); err != nil {
		fatal("page did not finish loading within %s: %v%s", defaultTimeout, err, navHint(err))
	}
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdForward(args []string) {
	_, _, page := withPage()
	if err := page.NavigateForward(); err != nil {
		fatal("forward failed: %v%s", err, navHint(err))
	}
	if err := page.WaitLoad(); err != nil {
		fatal("page did not finish loading within %s: %v%s", defaultTimeout, err, navHint(err))
	}
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdReload(args []string) {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hard := fs.Bool("hard", false, "")
	timeoutSecs := fs.Float64("timeout", 0, "")
	if err := fs.Parse(args); err != nil {
		fatal("unknown flag: %s\nusage: rodney reload [--hard] [--timeout SEC]", findUnknownFlag(args, fs))
	}
	timeout := resolveTimeout(*timeoutSecs)
	_, _, page := withPageTimeout(timeout)
	if *hard {
		// CDP Page.reload with ignoreCache (equivalent to Shift+Refresh)
		if err := (proto.PageReload{IgnoreCache: true}).Call(page); err != nil {
			fatal("reload failed: %v%s", err, navHint(err))
		}
	} else {
		if err := page.Reload(); err != nil {
			fatal("reload failed: %v%s", err, navHint(err))
		}
	}
	if err := page.WaitLoad(); err != nil {
		fatal("page did not finish loading within %s: %v%s", timeout, err, navHint(err))
	}
	fmt.Println("Reloaded")
}

func cmdClearCache(args []string) {
	_, _, page := withPage()
	err := (proto.NetworkClearBrowserCache{}).Call(page)
	if err != nil {
		fatal("clear cache failed: %v", err)
	}
	fmt.Println("Browser cache cleared")
}

func cmdURL(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.URL)
}

func cmdTitle(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.Title)
}

func cmdHTML(args []string) {
	_, _, page := withPage()
	if len(args) > 0 {
		el, err := page.Element(args[0])
		if err != nil {
			fatal("element not found: %v", err)
		}
		html, err := el.HTML()
		if err != nil {
			fatal("failed to get HTML: %v", err)
		}
		fmt.Println(html)
	} else {
		html := page.MustEval(`() => document.documentElement.outerHTML`).Str()
		fmt.Println(html)
	}
}

func cmdText(args []string) {
	timeout, pos := parseTimeoutArgs("text", args)
	if len(pos) < 1 {
		fatal("usage: rodney text <selector> [--timeout SEC]")
	}
	_, _, page := withPageTimeout(timeout)
	el, err := page.Element(pos[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	text, err := el.Text()
	if err != nil {
		fatal("failed to get text: %v", err)
	}
	fmt.Println(text)
}

func cmdAttr(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney attr <selector> <attribute>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	val := el.MustAttribute(args[1])
	if val == nil {
		fatal("attribute %q not found", args[1])
	} else {
		fmt.Println(*val)
	}
}

func cmdPDF(args []string) {
	file := "page.pdf"
	if len(args) > 0 {
		file = args[0]
	}
	_, _, page := withPage()
	req := proto.PagePrintToPDF{}
	r, err := page.PDF(&req)
	if err != nil {
		fatal("failed to generate PDF: %v", err)
	}
	buf := make([]byte, 0)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	if err := os.WriteFile(file, buf, 0644); err != nil {
		fatal("failed to write PDF: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(buf))
}

func cmdJS(args []string) {
	// Flags must precede the expression (a JS expression can start with '-');
	// use '--' to pass such an expression literally.
	fs := flag.NewFlagSet("js", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeoutSecs := fs.Float64("timeout", 0, "")
	jsonOut := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		fatal("unknown flag: %s\nusage: rodney js [--timeout SEC] [--json] <expression>", findUnknownFlag(args, fs))
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fatal("usage: rodney js [--timeout SEC] [--json] <expression>")
	}
	expr := strings.Join(rest, " ")
	_, _, page := withPageTimeout(resolveTimeout(*timeoutSecs))

	out, err := evalJSExpr(page, expr, *jsonOut)
	if err != nil {
		fatal("JS error: %v", err)
	}
	fmt.Println(out)
}

func cmdClick(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney click <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		fatal("click failed: %v", err)
	}
	// Brief pause for click handlers to execute
	time.Sleep(100 * time.Millisecond)
	fmt.Println("Clicked")
}

func cmdInput(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney input <selector> <text>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	text := strings.Join(args[1:], " ")
	el.MustSelectAllText().MustInput(text)
	fmt.Printf("Typed: %s\n", text)
}

func cmdClear(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney clear <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustSelectAllText().MustInput("")
	fmt.Println("Cleared")
}

func cmdFile(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney file <selector> <path|->")
	}
	selector := args[0]
	filePath := args[1]

	_, _, page := withPage()
	el, err := page.Element(selector)
	if err != nil {
		fatal("element not found: %v", err)
	}

	if filePath == "-" {
		// Read from stdin to a temp file
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		tmp, err := os.CreateTemp("", "rodney-upload-*")
		if err != nil {
			fatal("failed to create temp file: %v", err)
		}
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			fatal("failed to write temp file: %v", err)
		}
		if err := tmp.Close(); err != nil {
			fatal("failed to close temp file: %v", err)
		}
		filePath = tmp.Name()
	} else {
		if _, err := os.Stat(filePath); err != nil {
			fatal("file not found: %v", err)
		}
	}

	if err := el.SetFiles([]string{filePath}); err != nil {
		fatal("failed to set file: %v", err)
	}
	fmt.Printf("Set file: %s\n", args[1])
}

func cmdDownload(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney download <selector> [file|-]")
	}
	selector := args[0]
	outFile := ""
	if len(args) > 1 {
		outFile = args[1]
	}

	_, _, page := withPage()
	el, err := page.Element(selector)
	if err != nil {
		fatal("element not found: %v", err)
	}

	// Get the URL from the element's href or src attribute
	urlStr := ""
	if v := el.MustAttribute("href"); v != nil {
		urlStr = *v
	} else if v := el.MustAttribute("src"); v != nil {
		urlStr = *v
	} else {
		fatal("element has no href or src attribute")
	}

	var data []byte

	if strings.HasPrefix(urlStr, "data:") {
		data, err = decodeDataURL(urlStr)
		if err != nil {
			fatal("failed to decode data URL: %v", err)
		}
	} else {
		// Use fetch() in the page context so it has cookies/session
		// Also resolves relative URLs automatically
		js := fmt.Sprintf(`async () => {
			const resp = await fetch(%q);
			if (!resp.ok) throw new Error('HTTP ' + resp.status);
			const buf = await resp.arrayBuffer();
			const bytes = new Uint8Array(buf);
			let binary = '';
			for (let i = 0; i < bytes.length; i++) {
				binary += String.fromCharCode(bytes[i]);
			}
			return btoa(binary);
		}`, urlStr)
		result, err := page.Eval(js)
		if err != nil {
			fatal("download failed: %v", err)
		}
		data, err = base64.StdEncoding.DecodeString(result.Value.Str())
		if err != nil {
			fatal("failed to decode response: %v", err)
		}
	}

	if outFile == "-" {
		if _, err := os.Stdout.Write(data); err != nil {
			fatal("failed to write output: %v", err)
		}
		return
	}

	if outFile == "" {
		outFile = inferDownloadFilename(urlStr)
	}

	if err := os.WriteFile(outFile, data, 0644); err != nil {
		fatal("failed to write file: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", outFile, len(data))
}

// decodeDataURL decodes a data:[<mediatype>][;base64],<data> URL.
func decodeDataURL(dataURL string) ([]byte, error) {
	// Find the comma separating metadata from data
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx < 0 {
		return nil, fmt.Errorf("invalid data URL: no comma found")
	}
	meta := dataURL[5:commaIdx] // skip "data:"
	encoded := dataURL[commaIdx+1:]

	if strings.HasSuffix(meta, ";base64") {
		return base64.StdEncoding.DecodeString(encoded)
	}
	// URL-encoded text
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return nil, err
	}
	return []byte(decoded), nil
}

// inferDownloadFilename tries to extract a reasonable filename from a URL.
func inferDownloadFilename(urlStr string) string {
	if strings.HasPrefix(urlStr, "data:") {
		// Extract MIME type for extension
		commaIdx := strings.Index(urlStr, ",")
		if commaIdx > 0 {
			meta := urlStr[5:commaIdx]
			meta = strings.TrimSuffix(meta, ";base64")
			ext := mimeToExt(meta)
			return nextAvailableFile("download", ext)
		}
		return nextAvailableFile("download", "")
	}

	parsed, err := url.Parse(urlStr)
	if err == nil && parsed.Path != "" && parsed.Path != "/" {
		base := filepath.Base(parsed.Path)
		if base != "." && base != "/" {
			return nextAvailableFile(
				strings.TrimSuffix(base, filepath.Ext(base)),
				filepath.Ext(base),
			)
		}
	}
	return nextAvailableFile("download", "")
}

// mimeToExt returns a file extension for common MIME types.
func mimeToExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/css":
		return ".css"
	case "application/json":
		return ".json"
	case "application/javascript":
		return ".js"
	case "application/octet-stream":
		return ".bin"
	default:
		return ""
	}
}

func cmdSelect(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney select <selector> <value>")
	}
	_, _, page := withPage()
	// Use JavaScript to set the value, as rod's Select matches by text
	js := fmt.Sprintf(`() => {
		const el = document.querySelector(%q);
		if (!el) throw new Error('element not found');
		el.value = %q;
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return el.value;
	}`, args[0], args[1])
	result, err := page.Eval(js)
	if err != nil {
		fatal("select failed: %v", err)
	}
	fmt.Printf("Selected: %s\n", result.Value.Str())
}

func cmdSubmit(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney submit <selector>")
	}
	_, _, page := withPage()
	_, err := page.Element(args[0])
	if err != nil {
		fatal("form not found: %v", err)
	}
	page.MustEval(fmt.Sprintf(`() => document.querySelector(%q).submit()`, args[0]))
	fmt.Println("Submitted")
}

func cmdHover(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney hover <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustHover()
	fmt.Println("Hovered")
}

func cmdFocus(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney focus <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustFocus()
	fmt.Println("Focused")
}

func cmdWait(args []string) {
	timeout, pos := parseTimeoutArgs("wait", args)
	if len(pos) < 1 {
		fatal("usage: rodney wait <selector> [--timeout SEC]")
	}
	_, _, page := withPageTimeout(timeout)
	el, err := page.Element(pos[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	if err := el.WaitVisible(); err != nil {
		fatal("element did not become visible within %s: %v%s", timeout, err, navHint(err))
	}
	fmt.Println("Element visible")
}

func cmdWaitLoad(args []string) {
	timeout, _ := parseTimeoutArgs("waitload", args)
	_, _, page := withPageTimeout(timeout)
	if err := page.WaitLoad(); err != nil {
		fatal("page did not finish loading within %s: %v%s", timeout, err, navHint(err))
	}
	fmt.Println("Page loaded")
}

func cmdWaitStable(args []string) {
	timeout, _ := parseTimeoutArgs("waitstable", args)
	_, _, page := withPageTimeout(timeout)
	if err := page.WaitStable(time.Second); err != nil {
		fatal("DOM did not stabilize within %s: %v%s", timeout, err, navHint(err))
	}
	fmt.Println("DOM stable")
}

func cmdWaitIdle(args []string) {
	timeout, _ := parseTimeoutArgs("waitidle", args)
	_, _, page := withPageTimeout(timeout)
	if err := page.WaitIdle(timeout); err != nil {
		fatal("network did not become idle within %s: %v%s", timeout, err, navHint(err))
	}
	fmt.Println("Network idle")
}

func cmdSleep(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney sleep <seconds>")
	}
	secs, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		fatal("invalid seconds: %v", err)
	}
	time.Sleep(time.Duration(secs * float64(time.Second)))
}

// nextAvailableFile returns "base+ext" if it doesn't exist,
// otherwise "base-2+ext", "base-3+ext", etc.
func nextAvailableFile(base, ext string) string {
	name := base + ext
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return name
	}
	for i := 2; ; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name
		}
	}
}

func cmdScreenshot(args []string) {
	fs := flag.NewFlagSet("screenshot", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	width := fs.Int("width", 1280, "")
	fs.IntVar(width, "w", 1280, "")
	height := fs.Int("height", 0, "")
	fs.IntVar(height, "h", 0, "")

	if err := fs.Parse(args); err != nil {
		fatal("%v", err)
	}

	fullPage := true
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "height" || f.Name == "h" {
			fullPage = false
		}
	})

	var file string
	if fs.NArg() > 0 {
		file = fs.Arg(0)
	} else {
		file = nextAvailableFile("screenshot", ".png")
	}

	_, _, page := withPage()

	// Set viewport size
	viewportHeight := *height
	if viewportHeight == 0 {
		viewportHeight = 720
	}
	err := proto.EmulationSetDeviceMetricsOverride{
		Width:             *width,
		Height:            viewportHeight,
		DeviceScaleFactor: 1,
	}.Call(page)
	if err != nil {
		fatal("failed to set viewport: %v", err)
	}

	data, err := page.Screenshot(fullPage, nil)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Println(file)
}

func cmdScreenshotEl(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney screenshot-el <selector> [file]")
	}
	file := "element.png"
	if len(args) > 1 {
		file = args[1]
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(data))
}

func cmdPages(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := orderedPages(browser)
	if err != nil {
		fatal("%v", err)
	}
	activeID := ""
	if p, err := getActivePage(browser, s); err == nil {
		activeID = string(p.TargetID)
	}
	for i, p := range pages {
		marker := " "
		if string(p.TargetID) == activeID {
			marker = "*"
		}
		info, _ := p.Info()
		if info != nil {
			fmt.Printf("%s [%d] %s  %s - %s\n", marker, i, p.TargetID, info.Title, info.URL)
		} else {
			fmt.Printf("%s [%d] %s  (unknown)\n", marker, i, p.TargetID)
		}
	}
}

func cmdPage(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney page <index|target-id>")
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := orderedPages(browser)
	if err != nil {
		fatal("%v", err)
	}
	// Accept either a positional index or a (stable) target id, so callers can
	// switch by the id printed by `pages`/`newpage` and not worry about ordering.
	idx, err := strconv.Atoi(args[0])
	if err != nil {
		idx, err = matchTargetID(targetIDs(pages), args[0])
		if err != nil {
			fatal("%v", err)
		}
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range (0-%d)", idx, len(pages)-1)
	}
	setActivePage(s, pages, idx)
	if err := saveState(s); err != nil {
		fatal("failed to save state: %v", err)
	}
	info, _ := pages[idx].Info()
	if info != nil {
		fmt.Printf("Switched to [%d] %s - %s\n", idx, info.Title, info.URL)
	}
}

func cmdNewPage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	url := ""
	if len(args) > 0 {
		url = args[0]
		if !strings.Contains(url, "://") {
			url = "http://" + url
		}
	}

	var page *rod.Page
	if url != "" {
		page, err = browser.Page(proto.TargetCreateTarget{URL: url})
		if err != nil {
			fatal("failed to open page: %v%s", err, navHint(err))
		}
		page = page.Timeout(defaultTimeout)
		if err := page.WaitLoad(); err != nil {
			fatal("page did not finish loading within %s: %v%s", defaultTimeout, err, navHint(err))
		}
	} else {
		page, err = browser.Page(proto.TargetCreateTarget{})
		if err != nil {
			fatal("failed to open page: %v%s", err, navHint(err))
		}
	}

	// Switch active to the new page, keyed by its stable target id.
	pages, _ := orderedPages(browser)
	for i, p := range pages {
		if p.TargetID == page.TargetID {
			setActivePage(s, pages, i)
			break
		}
	}
	_ = saveState(s)

	info, _ := page.Info()
	url = ""
	if info != nil {
		url = info.URL
	}
	fmt.Printf("Opened [%d] %s\n", s.ActivePage, url)
	// Print the target id on its own line so an agent can capture it and pin
	// later commands with --target / RODNEY_TARGET.
	fmt.Printf("target: %s\n", page.TargetID)
}

func cmdClosePage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := orderedPages(browser)
	if err != nil {
		fatal("%v", err)
	}
	if len(pages) <= 1 {
		fatal("cannot close the last page")
	}

	// Default to the resolved active tab; otherwise an index or a target id.
	idx := -1
	if len(args) > 0 {
		idx, err = strconv.Atoi(args[0])
		if err != nil {
			idx, err = matchTargetID(targetIDs(pages), args[0])
			if err != nil {
				fatal("%v", err)
			}
		}
	} else if active, aerr := getActivePage(browser, s); aerr == nil {
		idx = slices.IndexFunc(pages, func(p *rod.Page) bool { return p.TargetID == active.TargetID })
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range (0-%d)", idx, len(pages)-1)
	}

	closedID := string(pages[idx].TargetID)
	pages[idx].MustClose()

	// Re-resolve the active tab. If we closed it (or nothing was tracked),
	// default to the first remaining tab; otherwise keep pointing at the same id.
	remaining, _ := orderedPages(browser)
	if len(remaining) > 0 {
		if s.ActiveTarget == "" || s.ActiveTarget == closedID {
			setActivePage(s, remaining, 0)
		} else if i := slices.IndexFunc(remaining, func(p *rod.Page) bool {
			return string(p.TargetID) == s.ActiveTarget
		}); i >= 0 {
			setActivePage(s, remaining, i)
		} else {
			setActivePage(s, remaining, 0)
		}
	}
	_ = saveState(s)
	fmt.Printf("Closed page %d\n", idx)
}

func cmdExists(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney exists <selector>")
	}
	_, _, page := withPage()
	has, _, err := page.Has(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	if has {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

func cmdCount(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney count <selector>")
	}
	_, _, page := withPage()
	els, err := page.Elements(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	fmt.Println(len(els))
}

func cmdVisible(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney visible <selector>")
	}
	_, _, page := withPage()
	el, err := page.Element(args[0])
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	visible, err := el.Visible()
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	if visible {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

// parseAssertArgs separates flags (--message/-m) from positional args.
// Returns (expression, expected, message). expected is nil for truthy mode.
func parseAssertArgs(args []string) (expr string, expected *string, message string) {
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message", "-m":
			i++
			if i < len(args) {
				message = args[i]
			}
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) >= 1 {
		expr = positional[0]
	}
	if len(positional) >= 2 {
		expected = &positional[1]
	}
	return
}

// formatAssertFail builds the failure output line.
// For truthy failures expected is nil; for equality failures it points to the expected string.
func formatAssertFail(actual string, expected *string, message string) string {
	if expected != nil {
		// Equality mode
		detail := fmt.Sprintf("got %q, expected %q", actual, *expected)
		if message != "" {
			return fmt.Sprintf("fail: %s (%s)", message, detail)
		}
		return fmt.Sprintf("fail: %s", detail)
	}
	// Truthy mode
	if message != "" {
		return fmt.Sprintf("fail: %s (got %s)", message, actual)
	}
	return fmt.Sprintf("fail: got %s", actual)
}

func cmdAssert(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney assert <js-expression> [expected] [--message msg]")
	}

	expr, expected, message := parseAssertArgs(args)
	if expr == "" {
		fatal("usage: rodney assert <js-expression> [expected] [--message msg]")
	}

	_, _, page := withPage()

	js := fmt.Sprintf(`() => { return (%s); }`, expr)
	result, err := page.Eval(js)
	if err != nil {
		fatal("JS error: %v", err)
	}

	// Format the result value as a string, matching the js command's output
	v := result.Value
	raw := v.JSON("", "")
	var actual string
	switch {
	case raw == "null" || raw == "undefined":
		actual = raw
	case raw == "true" || raw == "false":
		actual = raw
	case len(raw) > 0 && raw[0] == '"':
		actual = v.Str()
	case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
		actual = v.JSON("", "  ")
	default:
		actual = raw
	}

	if expected != nil {
		// Equality mode: compare string representation to expected
		if actual == *expected {
			fmt.Println("pass")
			os.Exit(0)
		} else {
			fmt.Println(formatAssertFail(actual, expected, message))
			os.Exit(1)
		}
	} else {
		// Truthy mode: check if the JS value is truthy
		switch raw {
		case "false", "0", "null", "undefined", `""`:
			fmt.Println(formatAssertFail(actual, nil, message))
			os.Exit(1)
		default:
			fmt.Println("pass")
			os.Exit(0)
		}
	}
}

// Ignore SIGPIPE for piped output
func init() {
	signal.Ignore(syscall.SIGPIPE)
}

// --- Accessibility commands ---

func cmdAXTree(args []string) {
	fs := flag.NewFlagSet("ax-tree", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	depthVal := fs.Int("depth", 0, "")
	jsonOutput := fs.Bool("json", false, "")

	if err := fs.Parse(args); err != nil {
		fatal("unknown flag: %s\nusage: rodney ax-tree [--depth N] [--json]", findUnknownFlag(args, fs))
	}
	if fs.NArg() > 0 {
		fatal("unknown flag: %s\nusage: rodney ax-tree [--depth N] [--json]", fs.Arg(0))
	}

	var depth *int
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "depth" {
			depth = depthVal
		}
	})

	_, _, page := withPage()
	result, err := proto.AccessibilityGetFullAXTree{Depth: depth}.Call(page)
	if err != nil {
		fatal("failed to get accessibility tree: %v", err)
	}

	if *jsonOutput {
		fmt.Println(formatAXTreeJSON(result.Nodes))
	} else {
		fmt.Print(formatAXTree(result.Nodes))
	}
}

func cmdAXFind(args []string) {
	fs := flag.NewFlagSet("ax-find", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "")
	role := fs.String("role", "", "")
	jsonOutput := fs.Bool("json", false, "")

	if err := fs.Parse(args); err != nil {
		fatal("unknown flag: %s\nusage: rodney ax-find [--name N] [--role R] [--json]", findUnknownFlag(args, fs))
	}
	if fs.NArg() > 0 {
		fatal("unknown flag: %s\nusage: rodney ax-find [--name N] [--role R] [--json]", fs.Arg(0))
	}

	_, _, page := withPage()
	nodes, err := queryAXNodes(page, *name, *role)
	if err != nil {
		fatal("query failed: %v", err)
	}

	if len(nodes) == 0 {
		fmt.Fprintln(os.Stderr, "No matching nodes")
		os.Exit(1)
	}

	if *jsonOutput {
		data, _ := json.MarshalIndent(nodes, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(formatAXNodeList(nodes))
	}
}

func cmdAXNode(args []string) {
	// Pre-extract --json since it may appear after the positional selector
	jsonOutput := false
	var filtered []string
	for _, a := range args {
		if a == "--json" {
			jsonOutput = true
		} else {
			filtered = append(filtered, a)
		}
	}

	fs := flag.NewFlagSet("ax-node", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(filtered); err != nil {
		fatal("%v", err)
	}

	if fs.NArg() < 1 {
		fatal("usage: rodney ax-node <selector> [--json]")
	}
	selector := fs.Arg(0)

	_, _, page := withPage()
	node, err := getAXNode(page, selector)
	if err != nil {
		fatal("%v", err)
	}

	if jsonOutput {
		fmt.Println(formatAXNodeDetailJSON(node))
	} else {
		fmt.Print(formatAXNodeDetail(node))
	}
}

// queryAXNodes uses Accessibility.queryAXTree to find nodes by name and/or role.
func queryAXNodes(page *rod.Page, name, role string) ([]*proto.AccessibilityAXNode, error) {
	// Get the document node to use as query root
	zero := 0
	doc, err := proto.DOMGetDocument{Depth: &zero}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	result, err := proto.AccessibilityQueryAXTree{
		BackendNodeID:  doc.Root.BackendNodeID,
		AccessibleName: name,
		Role:           role,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("accessibility query failed: %w", err)
	}

	return result.Nodes, nil
}

// getAXNode gets the accessibility node for a DOM element identified by CSS selector.
func getAXNode(page *rod.Page, selector string) (*proto.AccessibilityAXNode, error) {
	el, err := page.Element(selector)
	if err != nil {
		return nil, fmt.Errorf("element not found: %w", err)
	}

	// Describe the DOM node to get its backend node ID
	node, err := proto.DOMDescribeNode{ObjectID: el.Object.ObjectID}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to describe DOM node: %w", err)
	}

	result, err := proto.AccessibilityGetPartialAXTree{
		BackendNodeID:  node.Node.BackendNodeID,
		FetchRelatives: false,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get accessibility info: %w", err)
	}

	// Find the non-ignored node (the first non-ignored node is typically our target)
	for _, n := range result.Nodes {
		if !n.Ignored {
			return n, nil
		}
	}

	// Fall back to first node if all are ignored
	if len(result.Nodes) > 0 {
		return result.Nodes[0], nil
	}

	return nil, fmt.Errorf("no accessibility node found for selector %q", selector)
}

// axValueStr extracts a printable string from an AccessibilityAXValue.
func axValueStr(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	raw := v.Value.JSON("", "")
	// Unquote JSON strings
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s
		}
	}
	return raw
}

// formatAXTree formats a flat list of AX nodes as an indented text tree.
// Ignored nodes are skipped.
func formatAXTree(nodes []*proto.AccessibilityAXNode) string {
	if len(nodes) == 0 {
		return ""
	}

	// Build lookup maps
	nodeByID := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode)
	for _, n := range nodes {
		nodeByID[n.NodeID] = n
	}

	// Find root (node with no parent or first node)
	var rootID proto.AccessibilityAXNodeID
	for _, n := range nodes {
		if n.ParentID == "" {
			rootID = n.NodeID
			break
		}
	}
	if rootID == "" && len(nodes) > 0 {
		rootID = nodes[0].NodeID
	}

	var sb strings.Builder
	var walk func(id proto.AccessibilityAXNodeID, depth int)
	walk = func(id proto.AccessibilityAXNodeID, depth int) {
		node, ok := nodeByID[id]
		if !ok {
			return
		}
		// Skip ignored nodes but still recurse into their children
		if !node.Ignored {
			indent := strings.Repeat("  ", depth)
			role := axValueStr(node.Role)
			name := axValueStr(node.Name)

			line := fmt.Sprintf("%s[%s]", indent, role)
			if name != "" {
				line += fmt.Sprintf(" %q", name)
			}

			// Append interesting properties
			props := formatProperties(node.Properties)
			if props != "" {
				line += " (" + props + ")"
			}

			sb.WriteString(line + "\n")
			// Children at depth+1
			for _, childID := range node.ChildIDs {
				walk(childID, depth+1)
			}
		} else {
			// Ignored node: pass through to children at same depth
			for _, childID := range node.ChildIDs {
				walk(childID, depth)
			}
		}
	}

	walk(rootID, 0)
	return sb.String()
}

// formatProperties formats the interesting AX properties into a comma-separated string.
func formatProperties(props []*proto.AccessibilityAXProperty) string {
	if len(props) == 0 {
		return ""
	}
	var parts []string
	for _, p := range props {
		val := axValueStr(p.Value)
		switch string(p.Name) {
		case "focusable", "disabled", "editable", "hidden", "required",
			"checked", "expanded", "selected", "modal", "multiline",
			"multiselectable", "readonly", "focused", "settable":
			// Boolean-ish properties: only show if true
			if val == "true" {
				parts = append(parts, string(p.Name))
			}
		case "level":
			parts = append(parts, fmt.Sprintf("level=%s", val))
		case "autocomplete", "hasPopup", "orientation", "live",
			"relevant", "valuemin", "valuemax", "valuetext",
			"roledescription", "keyshortcuts":
			if val != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", p.Name, val))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// formatAXTreeJSON formats nodes as a JSON array.
func formatAXTreeJSON(nodes []*proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(data)
}

// formatAXNodeList formats a list of nodes as single-line summaries.
func formatAXNodeList(nodes []*proto.AccessibilityAXNode) string {
	var sb strings.Builder
	for _, node := range nodes {
		role := axValueStr(node.Role)
		name := axValueStr(node.Name)
		line := fmt.Sprintf("[%s]", role)
		if name != "" {
			line += fmt.Sprintf(" %q", name)
		}
		if node.BackendDOMNodeID != 0 {
			line += fmt.Sprintf(" backendNodeId=%d", node.BackendDOMNodeID)
		}
		props := formatProperties(node.Properties)
		if props != "" {
			line += " (" + props + ")"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// formatAXNodeDetail formats a single node with all its properties in key: value format.
func formatAXNodeDetail(node *proto.AccessibilityAXNode) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "role: %s\n", axValueStr(node.Role))
	if name := axValueStr(node.Name); name != "" {
		fmt.Fprintf(&sb, "name: %s\n", name)
	}
	if desc := axValueStr(node.Description); desc != "" {
		fmt.Fprintf(&sb, "description: %s\n", desc)
	}
	if val := axValueStr(node.Value); val != "" {
		fmt.Fprintf(&sb, "value: %s\n", val)
	}
	for _, p := range node.Properties {
		val := axValueStr(p.Value)
		fmt.Fprintf(&sb, "%s: %s\n", p.Name, val)
	}
	return sb.String()
}

// formatAXNodeDetailJSON formats a single node as JSON.
func formatAXNodeDetailJSON(node *proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(node, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// --- Auth proxy for environments with authenticated HTTP proxies ---

// detectProxy checks for HTTPS_PROXY/HTTP_PROXY with credentials.
// Returns (proxyServer, username, password, true) if auth proxy is needed.
func detectProxy() (server, user, pass string, needed bool) {
	proxyEnv := os.Getenv("HTTPS_PROXY")
	if proxyEnv == "" {
		proxyEnv = os.Getenv("https_proxy")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("HTTP_PROXY")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("http_proxy")
	}
	if proxyEnv == "" {
		return "", "", "", false
	}
	parsed, err := url.Parse(proxyEnv)
	if err != nil || parsed.User == nil {
		return "", "", "", false
	}
	user = parsed.User.Username()
	pass, _ = parsed.User.Password()
	if user == "" {
		return "", "", "", false
	}
	server = parsed.Hostname() + ":" + parsed.Port()
	return server, user, pass, true
}

// cmdInternalProxy is a hidden subcommand: rodney _proxy <port> <upstream> <authHeader>
// It runs a local auth proxy that forwards to the upstream proxy with credentials.
func cmdInternalProxy(args []string) {
	if len(args) < 3 {
		fatal("usage: rodney _proxy <port> <upstream> <authHeader>")
	}
	port := args[0]
	upstream := args[1]
	authHeader := args[2]

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fatal("proxy listen failed: %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				proxyConnect(w, r, upstream, authHeader)
			} else {
				proxyHTTP(w, r, upstream, authHeader)
			}
		}),
	}
	if err := server.Serve(listener); err != nil { // blocks until the server stops
		fatal("proxy server failed: %v", err)
	}
}

func proxyConnect(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	upstreamConn, err := net.DialTimeout("tcp", upstream, 30*time.Second)
	if err != nil {
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		r.Host, r.Host, authHeader)
	if _, err := upstreamConn.Write([]byte(connectReq)); err != nil {
		_ = upstreamConn.Close()
		http.Error(w, "upstream write failed", http.StatusBadGateway)
		return
	}

	buf := make([]byte, 4096)
	n, err := upstreamConn.Read(buf)
	if err != nil {
		_ = upstreamConn.Close()
		http.Error(w, "upstream read failed", http.StatusBadGateway)
		return
	}
	response := string(buf[:n])
	if len(response) < 12 || response[9:12] != "200" {
		_ = upstreamConn.Close()
		http.Error(w, "upstream rejected CONNECT", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		return
	}

	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		_, _ = io.Copy(upstreamConn, clientConn)
		_ = upstreamConn.Close()
	}()
	go func() {
		_, _ = io.Copy(clientConn, upstreamConn)
		_ = clientConn.Close()
	}()
}

func proxyHTTP(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	proxyURL, _ := url.Parse("http://" + upstream)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		ProxyConnectHeader: http.Header{
			"Proxy-Authorization": {authHeader},
		},
	}
	r.Header.Set("Proxy-Authorization", authHeader)

	resp, err := transport.RoundTrip(r)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
