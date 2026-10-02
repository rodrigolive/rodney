package main

import (
	"net/url"
	"os"
	"strings"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Per-site provisions.
//
// Reddit, measured in October 2026 from the residential IP the rews crawler
// relays through: Chrome 146 and 154 that send User-Agent client hints
// (Sec-CH-UA headers, navigator.userAgentData) get "Prove your humanity" on
// the first response, headless or headed, Chromium or Google Chrome, and
// whatever else the fingerprint says. The same browsers without client hints
// pass Reddit's JS challenge (a redirect to ?solution=…) and get the page;
// that is what rodney sent before identity moved to launch time, by accident
// of an override without metadata, and what Firefox and Safari send. So on
// Reddit's hosts every command withholds client hints: a User-Agent override
// with the session's own UA and languages but no client-hint metadata. Like
// any CDP override it lapses when the command exits, after Reddit has decided.

// defaultNoClientHintsHosts get no client hints unless RODNEY_NO_CLIENT_HINTS
// says otherwise. Subdomains (www., old., new.) are included.
var defaultNoClientHintsHosts = []string{"reddit.com", "redd.it", "redditmedia.com"}

// noClientHintsHosts returns the hosts that get no client hints:
// RODNEY_NO_CLIENT_HINTS (comma-separated; "none" for none) or the defaults.
func noClientHintsHosts() []string {
	v, set := os.LookupEnv("RODNEY_NO_CLIENT_HINTS")
	if !set || strings.TrimSpace(v) == "" {
		return defaultNoClientHintsHosts
	}
	if strings.EqualFold(strings.TrimSpace(v), "none") {
		return nil
	}
	var hosts []string
	for _, h := range strings.Split(v, ",") {
		if h = strings.Trim(strings.ToLower(strings.TrimSpace(h)), "."); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

// withholdsClientHints reports whether a page at rawURL gets no client hints.
func withholdsClientHints(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range noClientHintsHosts() {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// withholdClientHints overrides page's User-Agent for this command without
// client-hint metadata, which makes Chrome drop the Sec-CH-UA headers and
// blank navigator.userAgentData. The UA and languages stay the session's own,
// so nothing else about the request changes.
func withholdClientHints(page *rod.Page, s *State) error {
	ua := s.UserAgent
	if ua == "" {
		v, err := proto.BrowserGetVersion{}.Call(page.Browser())
		if err != nil {
			return err
		}
		ua = strings.Replace(v.UserAgent, "HeadlessChrome/", "Chrome/", 1)
	}
	langs := s.Lang
	if langs == "" {
		// An empty override would reset Accept-Language: keep the profile's.
		if res, err := page.Eval(`() => navigator.languages.join(",")`); err == nil {
			langs = res.Value.Str()
		}
	}
	return proto.NetworkSetUserAgentOverride{UserAgent: ua, AcceptLanguage: langs}.Call(page)
}

// applySiteProvisions applies what the page's current site needs for this
// command (see withholdClientHints). Best-effort: a page that can't be asked
// where it is gets nothing special.
func applySiteProvisions(page *rod.Page, s *State) {
	info, err := page.Info()
	if err != nil || !withholdsClientHints(info.URL) {
		return
	}
	_ = withholdClientHints(page, s)
}
