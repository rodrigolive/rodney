package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// handleHeaders echoes the request headers a fingerprinting server would see.
func handleHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	b, _ := json.Marshal(map[string]string{
		"ua":       r.UserAgent(),
		"secChUa":  r.Header.Get("Sec-CH-UA"),
		"platform": r.Header.Get("Sec-CH-UA-Platform"),
		"lang":     r.Header.Get("Accept-Language"),
	})
	_, _ = w.Write([]byte(`<!DOCTYPE html><html><body><pre id="h">` + string(b) + `</pre></body></html>`))
}

func TestDesktopUserAgent(t *testing.T) {
	cases := map[string]string{
		"linux":   "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36",
		"darwin":  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36",
		"windows": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/154.0.0.0 Safari/537.36",
	}
	for goos, want := range cases {
		if got := desktopUserAgent(goos, 154); got != want {
			t.Errorf("%s: got %q, want %q", goos, got, want)
		}
	}
}

func TestChromeMajorVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script stand-in for the browser")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "chromium")
	script := "#!/bin/sh\necho 'Chromium 154.0.8037.92 built on Debian GNU/Linux 12 (bookworm)'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := chromeMajorVersion(fake); got != 154 {
		t.Errorf("got %d, want 154", got)
	}
	if got := chromeMajorVersion(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("a missing binary should give 0, got %d", got)
	}
}

func TestConfigureIdentity_FallsBackWithoutVersion(t *testing.T) {
	l := launcher.New()
	ua, li := configureIdentity(l, startOpts{headless: true}, filepath.Join(t.TempDir(), "missing"), "linux")
	if ua != "" || li {
		t.Errorf("got (%q, %v), want no UA and per-command emulation", ua, li)
	}
	if l.Has("window-size") || l.Has("user-agent") {
		t.Error("the fallback must not size the window: emulation sets the viewport")
	}
	if l.Has("enable-automation") {
		t.Error("automation switches go in every case")
	}
}

func TestConfigureIdentity_HeadlessScreen(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "chrome")
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script stand-in for the browser")
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Google Chrome 154.0.8037.97'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"linux", "darwin"} {
		l := launcher.New()
		if _, li := configureIdentity(l, startOpts{headless: true}, fake, goos); !li {
			t.Fatalf("%s: expected a launch identity", goos)
		}
		// Current headless Chrome reports 800x600 on macOS too, under a
		// 1280x887 window.
		if got := l.Get("screen-info"); got != screenFor(headlessWindow) {
			t.Errorf("%s: screen-info = %q, want %q", goos, got, screenFor(headlessWindow))
		}
	}
}

func TestConfigureIdentity_Headed(t *testing.T) {
	l := launcher.New()
	ua, li := configureIdentity(l, startOpts{window: "1920x1080"}, "unused", "linux")
	if ua != "" || !li {
		t.Errorf("a headed window keeps its own UA and identity, got (%q, %v)", ua, li)
	}
	if got := l.Get("window-size"); got != "1920,1080" {
		t.Errorf("window-size = %q", got)
	}
	if l.Has("force-device-scale-factor") || l.Has("screen-info") {
		t.Error("headed windows report the real screen")
	}
}

func TestLaunchWindow(t *testing.T) {
	for _, c := range []struct {
		in       string
		headless bool
		want     string
		bad      bool
	}{
		{"", true, headlessWindow, false},
		{"", false, "", false},
		{"1920x1080", false, "1920,1080", false},
		{"1366,768", true, "1366,768", false},
		{"big", true, "", true},
		{"10x10", true, "", true},
	} {
		got, err := launchWindow(c.in, c.headless)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("launchWindow(%q, %v) = (%q, %v)", c.in, c.headless, got, err)
		}
	}
}

func TestParseStartArgs_IdentityFlags(t *testing.T) {
	opts, err := parseStartArgs([]string{"--stealth", "--timezone", "Europe/Madrid", "--lang", "en-US,es", "--window", "1920x1080", "--chrome-arg=--use-angle=gl", "--chrome-arg", "--ignore-gpu-blocklist"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.human || !opts.noConsole {
		t.Error("--stealth implies --human and --no-console")
	}
	if opts.timezone != "Europe/Madrid" || opts.lang != "en-US,es" || opts.window != "1920x1080" {
		t.Errorf("got %+v", opts)
	}
	if strings.Join(opts.chromeArgs, " ") != "--use-angle=gl --ignore-gpu-blocklist" {
		t.Errorf("chromeArgs = %v", opts.chromeArgs)
	}
	if _, err := parseStartArgs([]string{"--timezone", "Mars/Olympus"}); err == nil {
		t.Error("an unknown timezone should be refused (Chrome would silently use UTC)")
	}
	if _, err := parseStartArgs([]string{"--window", "huge"}); err == nil {
		t.Error("a malformed --window should be refused")
	}
}

func TestStartArgs_EnvDefaults(t *testing.T) {
	t.Setenv("RODNEY_START_FLAGS", "  --show --timezone Europe/Madrid ")
	got := startArgs([]string{"--replace"})
	if strings.Join(got, " ") != "--show --timezone Europe/Madrid --replace" {
		t.Errorf("got %v", got)
	}
	opts, err := parseStartArgs(got)
	if err != nil || opts.headless || !opts.replace || opts.timezone != "Europe/Madrid" {
		t.Errorf("got %+v, %v", opts, err)
	}
}

func TestApplyChromeArgs(t *testing.T) {
	l := launcher.New().Set("disable-features", "TranslateUI")
	if err := applyChromeArgs(l, []string{"--use-angle=gl", "ignore-gpu-blocklist", "--disable-features=Foo"}); err != nil {
		t.Fatal(err)
	}
	if l.Get("use-angle") != "gl" || !l.Has("ignore-gpu-blocklist") {
		t.Error("switches should be set as given")
	}
	if got, _ := l.GetFlags("disable-features"); strings.Join(got, ",") != "TranslateUI,Foo" {
		t.Errorf("feature lists should be appended to, got %q", got)
	}
	if err := applyChromeArgs(l, []string{"--=x"}); err == nil {
		t.Error("an empty switch name should be refused")
	}
}

func TestGpuDisabled(t *testing.T) {
	if !gpuDisabled(true, "linux") {
		t.Error("headless Linux keeps --disable-gpu (SwiftShader still gives WebGL)")
	}
	if gpuDisabled(true, "darwin") || gpuDisabled(false, "linux") {
		t.Error("--disable-gpu would leave macOS and headed windows with no WebGL")
	}
}

func TestProxiedViaEnv(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	if proxiedViaEnv("linux") {
		t.Error("no proxy variables: not proxied")
	}
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:3128")
	if !proxiedViaEnv("linux") {
		t.Error("Chrome on Linux uses HTTPS_PROXY")
	}
	if proxiedViaEnv("darwin") {
		t.Error("Chrome on macOS ignores proxy variables")
	}
}

func TestWriteProfilePrefs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Default", "Preferences")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"profile":{"name":"x"},"webrtc":{"multiple_routes_enabled":false},"intl":{"selected_languages":"en-US,en"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeProfilePrefs(dir, identityPrefs(true, "en-US,es")); err != nil {
		t.Fatal(err)
	}
	doc := readPrefs(t, path)
	if doc["profile"].(map[string]any)["name"] != "x" {
		t.Error("unrelated preferences must survive")
	}
	webrtc := doc["webrtc"].(map[string]any)
	if webrtc["ip_handling_policy"] != "disable_non_proxied_udp" || webrtc["multiple_routes_enabled"] != false {
		t.Errorf("webrtc = %v", webrtc)
	}
	if intl := doc["intl"].(map[string]any); intl["accept_languages"] != "en-US,es" || intl["selected_languages"] != "en-US,es" {
		t.Errorf("intl = %v (Chrome rebuilds accept_languages from selected_languages: both must change)", intl)
	}

	// Not proxied any more: the policy goes, the rest stays.
	if err := writeProfilePrefs(dir, identityPrefs(false, "")); err != nil {
		t.Fatal(err)
	}
	doc = readPrefs(t, path)
	if _, has := doc["webrtc"].(map[string]any)["ip_handling_policy"]; has {
		t.Error("the WebRTC policy should be removed when the launch isn't proxied")
	}
	if doc["intl"].(map[string]any)["accept_languages"] != "en-US,es" {
		t.Error("languages are kept until --lang changes them")
	}

	// A file Chrome would refuse is left alone.
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeProfilePrefs(dir, identityPrefs(true, "")); err == nil {
		t.Error("unparseable Preferences should be reported")
	}
	if data, _ := os.ReadFile(path); string(data) != "{broken" {
		t.Error("unparseable Preferences must not be overwritten")
	}
}

func readPrefs(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// TestLaunchIdentity_WhatPagesSee launches a browser the way 'rodney start'
// does and checks what a page and its server can observe, with no rodney
// command attached (no CDP overrides in effect).
func TestLaunchIdentity_WhatPagesSee(t *testing.T) {
	dir := t.TempDir()
	l := launcher.New().Set("no-sandbox").Leakless(false).UserDataDir(dir).Headless(true)
	if gpuDisabled(true, runtime.GOOS) {
		l.Set("disable-gpu")
	}
	if singleProcessSupported() {
		l.Set("single-process")
	}
	l = configureExperiments(l)
	bin, err := resolveChromeBin()
	if err != nil {
		t.Fatal(err)
	}
	l.Bin(bin)
	opts := startOpts{headless: true, lang: "es-ES,es,en-US,en", timezone: "Asia/Tokyo"}
	ua, li := configureIdentity(l, opts, bin, runtime.GOOS)
	if !li || ua == "" {
		t.Fatalf("expected a launch identity, got (%q, %v)", ua, li)
	}
	// A profile that has been used: Chrome rebuilds accept_languages from
	// selected_languages, so the change must win over an existing list.
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Default", "Preferences"), []byte(`{"intl":{"accept_languages":"en-US,en","selected_languages":"en-US,en"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeProfilePrefs(dir, identityPrefs(true, opts.lang)); err != nil {
		t.Fatal(err)
	}
	browser := rod.New().ControlURL(l.MustLaunch()).NoDefaultDevice().MustConnect()
	// Chrome is still writing the profile when Close returns; wait for it
	// to exit before the temp dir goes.
	t.Cleanup(func() { browser.MustClose(); l.Cleanup() })

	page := browser.MustPage(env.server.URL + "/headers").MustWaitLoad()
	var seen struct{ UA, SecChUa, Platform, Lang string }
	if err := json.Unmarshal([]byte(page.MustElement("#h").MustText()), &seen); err != nil {
		t.Fatal(err)
	}
	if seen.UA != ua || strings.Contains(seen.UA, "Headless") {
		t.Errorf("server saw UA %q, want %q", seen.UA, ua)
	}
	// Chrome may send only the first language (its ReduceAcceptLanguage
	// experiment); either way it must be the one asked for.
	if !strings.HasPrefix(seen.Lang, "es-ES") {
		t.Errorf("Accept-Language = %q", seen.Lang)
	}

	got := page.MustEval(`async () => {
		const workerUA = await new Promise(r => {
			const w = new Worker(URL.createObjectURL(new Blob(["postMessage(navigator.userAgent)"])));
			w.onmessage = e => r(e.data);
			setTimeout(() => r("timeout"), 3000);
		});
		const ice = await new Promise(r => {
			const out = [], pc = new RTCPeerConnection();
			pc.createDataChannel("x");
			pc.onicecandidate = e => e.candidate ? out.push(e.candidate.candidate) : r(out);
			pc.createOffer().then(o => pc.setLocalDescription(o));
			setTimeout(() => r(out), 3000);
		});
		return {ua: navigator.userAgent, workerUA, webdriver: navigator.webdriver,
			brands: (navigator.userAgentData?.brands || []).length,
			langs: navigator.languages.join(","), tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
			iw: innerWidth, ih: innerHeight, ow: outerWidth, oh: outerHeight, sw: screen.width, sh: screen.height,
			ice: ice.length};
	}`)
	if got.Get("webdriver").Bool() {
		t.Error("navigator.webdriver should be false")
	}
	if got.Get("ua").Str() != ua || got.Get("workerUA").Str() != ua {
		t.Errorf("page UA %q / worker UA %q, want %q everywhere", got.Get("ua").Str(), got.Get("workerUA").Str(), ua)
	}
	if got.Get("brands").Int() == 0 {
		t.Error("navigator.userAgentData.brands is empty, which real Chrome never is")
	}
	if !strings.HasPrefix(got.Get("langs").Str(), "es-ES") {
		t.Errorf("navigator.languages = %q", got.Get("langs").Str())
	}
	if got.Get("tz").Str() != "Asia/Tokyo" {
		t.Errorf("timezone = %q", got.Get("tz").Str())
	}
	if got.Get("iw").Int() != 1280 || got.Get("ih").Int() != 800 {
		t.Errorf("page area %dx%d, want 1280x800 as before", got.Get("iw").Int(), got.Get("ih").Int())
	}
	if got.Get("oh").Int() <= got.Get("ih").Int() || got.Get("ow").Int() < got.Get("iw").Int() {
		t.Errorf("window %dx%d must enclose the page area", got.Get("ow").Int(), got.Get("oh").Int())
	}
	if got.Get("sw").Int() < got.Get("ow").Int() || got.Get("sh").Int() < got.Get("oh").Int() {
		t.Errorf("screen %dx%d must enclose the window", got.Get("sw").Int(), got.Get("sh").Int())
	}
	if got.Get("ice").Int() != 0 {
		t.Errorf("behind a proxy WebRTC must not gather non-proxied candidates, got %d", got.Get("ice").Int())
	}

	// The Reddit provision (quirks.go): the same page without client hints,
	// everything else about the request unchanged.
	if err := withholdClientHints(page, &State{UserAgent: ua}); err != nil {
		t.Fatal(err)
	}
	page.MustNavigate(env.server.URL + "/headers").MustWaitLoad()
	var withheld struct{ UA, SecChUa, Lang string }
	if err := json.Unmarshal([]byte(page.MustElement("#h").MustText()), &withheld); err != nil {
		t.Fatal(err)
	}
	if withheld.SecChUa != "" || page.MustEval(`() => (navigator.userAgentData?.brands || []).length`).Int() != 0 {
		t.Errorf("client hints should be withheld, server saw Sec-CH-UA %q", withheld.SecChUa)
	}
	if withheld.UA != ua || withheld.Lang != seen.Lang {
		t.Errorf("withholding changed the UA (%q) or languages (%q, was %q)", withheld.UA, withheld.Lang, seen.Lang)
	}
}

func TestProxiedViaArgs(t *testing.T) {
	if !proxiedViaArgs([]string{"--use-angle=gl", "--proxy-server=http://127.0.0.1:3128"}) {
		t.Error("a --proxy-server chrome-arg means the launch is proxied (WebRTC policy needed)")
	}
	if proxiedViaArgs([]string{"--use-angle=gl"}) {
		t.Error("no proxy switch: not proxied")
	}
}

func TestScreenFor(t *testing.T) {
	for window, want := range map[string]string{
		"1280,887":  "{1920x1080}",
		"1920,1080": "{1920x1080}",
		"2560,1440": "{2560x1440}",
		"1920,1200": "{1920x1200}",
		"bad":       "{1920x1080}",
	} {
		if got := screenFor(window); got != want {
			t.Errorf("screenFor(%q) = %q, want %q (the window must fit its screen)", window, got, want)
		}
	}
}
