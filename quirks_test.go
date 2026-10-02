package main

import "testing"

func TestWithholdsClientHints(t *testing.T) {
	t.Setenv("RODNEY_NO_CLIENT_HINTS", "")
	for raw, want := range map[string]bool{
		"https://www.reddit.com/r/golang/":           true,
		"https://old.reddit.com/r/oil/comments/abc/": true,
		"https://reddit.com/":                        true,
		"https://i.redd.it/x.jpg":                    true,
		"http://www.reddit.com/search/?q=x":          true,
		"https://notreddit.com/":                     false,
		"https://reddit.com.evil.example/":           false,
		"https://news.ycombinator.com/":              false,
		"chrome://newtab/":                           false,
		"about:blank":                                false,
		"::not a url":                                false,
	} {
		if got := withholdsClientHints(raw); got != want {
			t.Errorf("withholdsClientHints(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestNoClientHintsHosts_Env(t *testing.T) {
	t.Setenv("RODNEY_NO_CLIENT_HINTS", " Example.com, .news.example ,")
	if !withholdsClientHints("https://www.example.com/") || !withholdsClientHints("https://a.news.example/") {
		t.Error("hosts from RODNEY_NO_CLIENT_HINTS (any case, stray dots and spaces) should apply")
	}
	if withholdsClientHints("https://www.reddit.com/") {
		t.Error("RODNEY_NO_CLIENT_HINTS replaces the defaults")
	}
	t.Setenv("RODNEY_NO_CLIENT_HINTS", "none")
	if withholdsClientHints("https://www.reddit.com/") {
		t.Error(`"none" turns the provision off`)
	}
}
