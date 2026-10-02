package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
)

// What a page can tell about the browser it runs in is decided at launch.
//
// rodney is one process per command, and Chrome ties every CDP override
// (User-Agent, device metrics, locale) to the DevTools session that set it, so
// it lapses the moment that command exits. A page loaded by 'rodney open'
// would see the overridden identity only while 'open' runs: the requests it
// makes afterwards and the scripts that read navigator later get the raw
// headless browser again (HeadlessChrome in the UA, an 800x600 screen). The
// override also sent no client-hint metadata, which blanks
// navigator.userAgentData and drops the Sec-CH-UA headers, something real
// Chrome never does. So a session's identity lives where it can't lapse:
// Chrome's command line, its environment, and the profile's Preferences.

// headlessWindow is the window a headless session gets: headless Chrome draws
// an 87px toolbar, so the page area is the 1280x800 that go-rod's device
// emulation used to give, and outerHeight > innerHeight as in a real window.
const headlessWindow = "1280,887"

// headlessScreen is the screen a headless session reports. Headless Chrome
// otherwise defaults to 800x600, smaller than its own window: on Linux with
// any build, and on macOS with current Chrome (rod's Chromium 128 on macOS
// ignores the switch and reports the real display, which is fine too).
const headlessScreen = "{1920x1080}"

var chromeVersionRE = regexp.MustCompile(`(\d+)\.\d+\.\d+\.\d+`)

// resolveChromeBin returns the browser 'start' launches: ROD_CHROME_BIN, else
// go-rod's managed Chromium (downloaded on first use, as Launch would).
func resolveChromeBin() (string, error) {
	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		return bin, nil
	}
	return launcher.NewBrowser().Get()
}

// chromeMajorVersion asks the browser binary for its version ("Chromium
// 154.0.8037.92 built on Debian GNU/Linux 12", "Google Chrome 146.0.7680.178")
// and returns the major number, or 0 if it can't tell.
func chromeMajorVersion(bin string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return 0
	}
	m := chromeVersionRE.FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// desktopUserAgent is the User-Agent Chrome <major> sends on goos when it is
// not headless. Chrome's reduced UA freezes the platform token and zeroes the
// minor versions, so for a given major version this is exact.
func desktopUserAgent(goos string, major int) string {
	platform := "X11; Linux x86_64"
	switch goos {
	case "darwin":
		platform = "Macintosh; Intel Mac OS X 10_15_7"
	case "windows":
		platform = "Windows NT 10.0; Win64; x64"
	}
	return fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36", platform, major)
}

// configureIdentity sets what pages can see of a launch: automation switches
// off, the languages and timezone asked for, and for a headless browser a
// desktop UA, window and screen on the command line. It returns the UA it put
// there ("" if none) and whether the launch carries the session's identity,
// which is false only for a headless browser whose version couldn't be read:
// that session falls back to per-command device emulation (connectBrowser).
func configureIdentity(l *launcher.Launcher, opts startOpts, bin, goos string) (ua string, launchIdentity bool) {
	hideAutomation(l)
	window, _ := launchWindow(opts.window, opts.headless)
	ua = opts.userAgent
	launchIdentity = !opts.headless // a window shows the browser as it is
	if opts.headless {
		if ua == "" {
			if major := chromeMajorVersion(bin); major > 0 {
				ua = desktopUserAgent(goos, major)
			}
		}
		launchIdentity = ua != ""
	}
	if ua != "" {
		l.Set("user-agent", ua)
	}
	if launchIdentity {
		if window != "" {
			l.Set("window-size", window)
		}
		if opts.headless {
			// Screenshots stay 1280x800 pixels on HiDPI Macs, as with the
			// emulated device this replaces.
			l.Set("force-device-scale-factor", "1")
			l.Set("screen-info", headlessScreen)
		}
	}
	if opts.lang != "" {
		l.Set("lang", strings.Split(opts.lang, ",")[0])
	}
	if env := startEnv(opts.timezone); env != nil {
		l.Env(env...)
	}
	return ua, launchIdentity
}

// hideAutomation removes the switches that announce automation to pages:
// --enable-automation sets navigator.webdriver and shows the "controlled by
// automated test software" bar, and the AutomationControlled blink feature
// sets navigator.webdriver on its own.
func hideAutomation(l *launcher.Launcher) {
	l.Delete("enable-automation")
	l.Set("disable-blink-features", "AutomationControlled")
}

// gpuDisabled reports whether 'start' passes --disable-gpu. Headless Chrome on
// Linux still gets WebGL from SwiftShader with it, and servers have no GPU to
// lose. Elsewhere it would leave a page with no WebGL at all, which no real
// browser has: headless macOS renders through Metal, and a headed window keeps
// whatever GPU (or software GL, see --chrome-arg) the desktop provides.
func gpuDisabled(headless bool, goos string) bool {
	return headless && goos == "linux"
}

// applyChromeArgs adds --chrome-arg switches ("--name" or "--name=value") to
// the launch. The feature lists are appended to, so a user's
// --disable-features doesn't undo the ones rod and configureExperiments set.
func applyChromeArgs(l *launcher.Launcher, args []string) error {
	for _, a := range args {
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "" {
			return fmt.Errorf("invalid --chrome-arg %q", a)
		}
		switch {
		case !hasValue:
			l.Set(flags.Flag(name))
		case name == "disable-features" || name == "enable-features":
			l.Append(flags.Flag(name), value)
		default:
			l.Set(flags.Flag(name), value)
		}
	}
	return nil
}

// proxiedViaEnv reports whether Chrome will send its traffic through a proxy
// named in the environment. Only Chrome on Linux reads these variables; on
// macOS and Windows it uses the system proxy settings.
func proxiedViaEnv(goos string) bool {
	if goos != "linux" {
		return false
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// profilePrefs are the Preferences entries 'start' manages, by dotted path. A
// nil value removes the entry.
type profilePrefs map[string]any

// identityPrefs returns the profile preferences for a launch.
//
// Behind a proxy, WebRTC still gathers candidates over plain UDP, and its STUN
// request reports the host's own public address to any page that asks: a
// crawler relayed through a home connection gives its datacenter IP away.
// disable_non_proxied_udp is the policy privacy extensions set; Chrome has no
// working switch for it, only this preference (or enterprise policy). It is
// cleared again when a later launch isn't proxied, so WebRTC works normally.
//
// langs sets the languages pages see (navigator.languages, Accept-Language).
// Headless Chrome ignores --lang and --accept-lang for both; the preference is
// what a user changes in settings, and it works headless and headed alike.
func identityPrefs(proxied bool, langs string) profilePrefs {
	p := profilePrefs{"webrtc.ip_handling_policy": nil}
	if proxied {
		p["webrtc.ip_handling_policy"] = "disable_non_proxied_udp"
	}
	if langs != "" {
		p["intl.accept_languages"] = langs
	}
	return p
}

// writeProfilePrefs merges prefs into <dataDir>/Default/Preferences, before
// Chrome starts (it rewrites the file on exit, keeping what it read). The file
// is replaced atomically, and left alone if it can't be parsed or nothing
// changes.
func writeProfilePrefs(dataDir string, prefs profilePrefs) error {
	path := filepath.Join(dataDir, "Default", "Preferences")
	doc := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return err
	}
	changed := false
	for key, value := range prefs {
		if setPref(doc, strings.Split(key, "."), value) {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".Preferences-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// setPref sets (or, for a nil value, deletes) doc[path...] and reports whether
// that changed anything.
func setPref(doc map[string]any, path []string, value any) bool {
	key := path[0]
	if len(path) == 1 {
		old, had := doc[key]
		if value == nil {
			delete(doc, key)
			return had
		}
		if had && fmt.Sprint(old) == fmt.Sprint(value) {
			return false
		}
		doc[key] = value
		return true
	}
	child, ok := doc[key].(map[string]any)
	if !ok {
		if value == nil {
			return false
		}
		child = map[string]any{}
		doc[key] = child
	}
	return setPref(child, path[1:], value)
}

// validTimezone reports whether tz is an IANA zone name Go (and so ICU, which
// Chrome uses) can resolve. An unknown TZ makes Chrome fall back to UTC without
// a word, which is worse than refusing to start.
func validTimezone(tz string) bool {
	if tz == "" || strings.HasPrefix(tz, ":") {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// startEnv returns the environment Chrome is launched with: rodney's own, with
// TZ set when --timezone asks for one (nil means inherit unchanged).
func startEnv(tz string) []string {
	if tz == "" {
		return nil
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TZ=") {
			env = append(env, kv)
		}
	}
	return append(env, "TZ="+tz)
}

// launchWindow returns the --window-size value for a launch: the one asked for
// with --window (WxH or W,H), else the headless default; "" for a headed
// window, which opens maximized instead.
func launchWindow(window string, headless bool) (string, error) {
	if window == "" {
		if headless {
			return headlessWindow, nil
		}
		return "", nil
	}
	w, h, ok := strings.Cut(strings.ReplaceAll(window, "x", ","), ",")
	wi, errW := strconv.Atoi(w)
	hi, errH := strconv.Atoi(h)
	if !ok || errW != nil || errH != nil || wi < 200 || hi < 200 {
		return "", fmt.Errorf("invalid --window %q (want WIDTHxHEIGHT, e.g. 1920x1080)", window)
	}
	return fmt.Sprintf("%d,%d", wi, hi), nil
}
