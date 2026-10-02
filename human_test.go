package main

import (
	"math"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// handleHuman serves a tall page that records the input events it gets.
func handleHuman(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(`<!DOCTYPE html><html><body style="margin:0;height:3000px">
<input id="name" style="position:absolute;top:100px;left:100px;width:300px;height:30px" value="old">
<button id="far" style="position:absolute;top:2400px;left:600px;width:160px;height:50px">Far</button>
<button id="right" style="position:absolute;top:150px;left:3000px;width:120px;height:40px">Right</button>
<script>
window.rec = {moves: [], wheel: 0, keys: [], down: 0, up: 0, clicked: null, trusted: true};
const t = e => { rec.trusted = rec.trusted && e.isTrusted };
addEventListener('mousemove', e => { t(e); rec.moves.push([e.clientX, e.clientY]) }, true);
addEventListener('wheel', e => { t(e); rec.wheel++ }, {capture: true, passive: true});
addEventListener('mousedown', e => { t(e); rec.down = performance.now() }, true);
addEventListener('mouseup', e => { t(e); rec.up = performance.now() }, true);
addEventListener('click', e => { t(e); rec.clicked = e.target.id }, true);
addEventListener('keydown', e => { t(e); rec.keys.push(e.key + (e.shiftKey ? '+shift' : '')) }, true);
</script></body></html>`))
}

func humanPage(t *testing.T) *rod.Page {
	t.Helper()
	t.Setenv("RODNEY_HOME", t.TempDir())
	page := env.browser.MustPage(env.server.URL + "/human").MustWaitLoad()
	t.Cleanup(func() { page.MustClose() })
	return page
}

func TestHumanClick_ScrollsMovesAndClicks(t *testing.T) {
	page := humanPage(t)
	if err := humanClick(page, page.MustElement("#far")); err != nil {
		t.Fatal(err)
	}
	rec := page.MustEval(`() => rec`)
	if rec.Get("clicked").Str() != "far" {
		t.Errorf("click landed on %q, want #far", rec.Get("clicked").Str())
	}
	if rec.Get("wheel").Int() == 0 {
		t.Error("an element 2400px down should be reached with the wheel, not a jump")
	}
	if n := len(rec.Get("moves").Arr()); n < 10 {
		t.Errorf("the pointer should travel a path, got %d mousemove events", n)
	}
	if held := rec.Get("up").Num() - rec.Get("down").Num(); held < 40 {
		t.Errorf("the button was held %.0fms; people hold it for 50-150ms", held)
	}
	if !rec.Get("trusted").Bool() {
		t.Error("every event should be trusted input")
	}
	if _, ok := loadPointer(page); !ok {
		t.Error("the pointer position should be kept for the next command")
	}
}

func TestHumanType_KeyByKey(t *testing.T) {
	page := humanPage(t)
	el := page.MustElement("#name")
	if err := humanClick(page, el); err != nil {
		t.Fatal(err)
	}
	if err := el.SelectAllText(); err != nil {
		t.Fatal(err)
	}
	text := "Hi, Bob! ñ"
	if err := humanType(page, text); err != nil {
		t.Fatal(err)
	}
	if got := el.MustProperty("value").Str(); got != text {
		t.Errorf("value = %q, want %q", got, text)
	}
	keys := page.MustEval(`() => rec.keys.join(" ")`).Str()
	for _, want := range []string{"Shift+shift H+shift", "i", ",", "!+shift"} {
		if !strings.Contains(keys, want) {
			t.Errorf("keydown events %q should include %q", keys, want)
		}
	}
	if err := humanKey(page, input.Backspace); err != nil {
		t.Fatal(err)
	}
	if got := el.MustProperty("value").Str(); got != "Hi, Bob! " {
		t.Errorf("after Backspace value = %q", got)
	}
}

func TestHumanWheel_ScrollsBy(t *testing.T) {
	page := humanPage(t)
	if err := humanWheel(page, 450); err != nil {
		t.Fatal(err)
	}
	page.MustWaitIdle()
	y := page.MustEval(`() => new Promise(r => setTimeout(() => r(scrollY), 400))`).Num()
	if math.Abs(y-450) > 5 {
		t.Errorf("scrollY = %.0f, want about 450", y)
	}
	if page.MustEval(`() => rec.wheel`).Int() < 4 {
		t.Error("450px should take several wheel notches")
	}
}

func TestBezierPath(t *testing.T) {
	a, b := point{10, 20}, point{610, 420}
	pts := bezierPath(a, b, 100, 40)
	if len(pts) != 41 {
		t.Fatalf("got %d points, want 41", len(pts))
	}
	if pts[0] != a || pts[len(pts)-1] != b {
		t.Errorf("path runs %v -> %v, want %v -> %v", pts[0], pts[len(pts)-1], a, b)
	}
	// Minimum-jerk timing: the first and last steps are short, the middle long.
	step := func(i int) float64 { return math.Hypot(pts[i+1].X-pts[i].X, pts[i+1].Y-pts[i].Y) }
	if step(0) >= step(20) || step(39) >= step(20) {
		t.Errorf("steps %.1f, %.1f, %.1f should be slow-fast-slow", step(0), step(20), step(39))
	}
}

func TestHumanPath_OvershootsLongMoves(t *testing.T) {
	a, b := point{0, 0}, point{1200, 700}
	for i := 0; i < 20; i++ {
		pts := humanPath(a, b, 50)
		if pts[0] != a || pts[len(pts)-1] != b {
			t.Fatalf("path must start at the pointer and end on the target")
		}
	}
	short := humanPath(point{0, 0}, point{100, 0}, 50)
	if short[len(short)-1] != (point{100, 0}) {
		t.Error("short moves end on the target")
	}
}

func TestMinimumJerkAndFitts(t *testing.T) {
	if minimumJerk(0) != 0 || minimumJerk(1) != 1 || math.Abs(minimumJerk(0.5)-0.5) > 1e-9 {
		t.Error("minimum jerk runs 0 -> 0.5 -> 1")
	}
	for i := 0; i < 50; i++ {
		if n := fittsSteps(800, 20); n < 15 || n > 100 {
			t.Fatalf("fittsSteps = %d, outside 15..100", n)
		}
	}
}

func TestKeyFor(t *testing.T) {
	for _, r := range "aZ1!~ ," {
		if _, ok := keyFor(r); !ok {
			t.Errorf("%q should be on the keyboard", r)
		}
	}
	for _, r := range "ñé😀\x01\n" {
		if _, ok := keyFor(r); ok {
			t.Errorf("%q should not be a printable key", r)
		}
	}
	if namedKeys['\n'].key != "Enter" || namedKeys['\t'].key != "Tab" {
		t.Error("newline and tab are typed as the Enter and Tab keys")
	}
}

func TestHumanType_EnterIsTheEnterKey(t *testing.T) {
	page := humanPage(t)
	page.MustElement("#name").MustFocus()
	if err := humanType(page, "a\n"); err != nil {
		t.Fatal(err)
	}
	if keys := page.MustEval(`() => rec.keys.join(" ")`).Str(); keys != "a Enter" {
		t.Errorf("keydown keys = %q, want \"a Enter\" (rod's own Enter reports key \"\\r\")", keys)
	}
}

func TestParseScrollArgs(t *testing.T) {
	if dy, sel, err := parseScrollArgs([]string{"down"}); err != nil || dy != 600 || sel != "" {
		t.Errorf("down = (%v, %q, %v)", dy, sel, err)
	}
	if dy, _, err := parseScrollArgs([]string{"up", "250"}); err != nil || dy != -250 {
		t.Errorf("up 250 = (%v, %v)", dy, err)
	}
	if _, sel, err := parseScrollArgs([]string{"#footer"}); err != nil || sel != "#footer" {
		t.Errorf("selector = (%q, %v)", sel, err)
	}
	for _, bad := range [][]string{nil, {"down", "-5"}, {"down", "x"}, {"down", "Inf"}, {"up", "NaN"}, {"#a", "10"}} {
		if _, _, err := parseScrollArgs(bad); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}

func TestHumanEnabled(t *testing.T) {
	t.Setenv("RODNEY_HUMAN", "")
	if humanEnabled(&State{}) || !humanEnabled(&State{Human: true}) {
		t.Error("the session's --human decides by default")
	}
	t.Setenv("RODNEY_HUMAN", "1")
	if !humanEnabled(&State{}) {
		t.Error("RODNEY_HUMAN=1 turns it on")
	}
	t.Setenv("RODNEY_HUMAN", "off")
	if humanEnabled(&State{Human: true}) {
		t.Error("RODNEY_HUMAN=off turns it off")
	}
}

func TestPointerFile_RejectsOddTargetIDs(t *testing.T) {
	t.Setenv("RODNEY_HOME", t.TempDir())
	for _, id := range []string{"../../etc/passwd", "a/b", ""} {
		if got := pointerFile(&rod.Page{TargetID: proto.TargetTargetID(id)}); got != "" {
			t.Errorf("target id %q must not become a path, got %q", id, got)
		}
	}
	if got := pointerFile(&rod.Page{TargetID: "B9B8B11C826582D7829A2DC694FA8CEC"}); !strings.HasPrefix(got, os.Getenv("RODNEY_HOME")) {
		t.Errorf("pointer file %q should live in the state dir", got)
	}
}

func TestHumanClick_ScrollsSideways(t *testing.T) {
	page := humanPage(t)
	if err := humanClick(page, page.MustElement("#right")); err != nil {
		t.Fatal(err)
	}
	if got := page.MustEval(`() => rec.clicked`).Str(); got != "right" {
		t.Errorf("click landed on %q, want #right (3000px to the right)", got)
	}
	if page.MustEval(`() => scrollX`).Num() == 0 {
		t.Error("the page should have scrolled sideways")
	}
}

func TestHumanFocusField_OverlayAndFocusTrap(t *testing.T) {
	t.Setenv("RODNEY_HOME", t.TempDir())
	page := env.browser.MustPage("data:text/html," + `<!DOCTYPE html><html><body style="margin:0">
<input id="f" value="x" style="position:absolute;top:50px;left:50px;width:200px;height:30px">
<div id="shield" style="position:fixed;inset:0;background:rgba(0,0,0,.01)"></div></body></html>`).MustWaitLoad()
	t.Cleanup(func() { page.MustClose() })
	el := page.MustElement("#f")
	if err := humanFocusField(page, el); err != nil {
		t.Errorf("a field under a passive overlay should still get focus, got %v", err)
	}

	// A consent dialog that keeps focus: typing would go into it.
	page.MustEval(`() => {
		const d = document.createElement("dialog");
		d.innerHTML = "<button>OK</button>";
		document.body.append(d);
		d.showModal();
	}`)
	if err := humanFocusField(page, el); err == nil {
		t.Error("a modal dialog makes the page inert: the field must refuse")
	}
}
