package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// Humanlike input, for sessions started with --human (or --stealth) and for
// any command run with RODNEY_HUMAN=1.
//
// rod's input is mechanical: the pointer jumps to the exact centre of an
// element, the button goes down and up in the same millisecond, text arrives
// through Input.insertText with no key events at all, and elements jump into
// view. Behavioural bot checks look for exactly that. Here the pointer travels
// a curved path whose length in steps follows Fitts's law, overshooting long
// moves and correcting, as Xetera/ghost-cursor does; its speed follows the
// minimum-jerk profile of human reaching movements (slow, fast, slow). It
// clicks a random point inside the element and holds the button for a human
// interval, text is typed key by key at a typist's cadence, and elements are
// brought into view with the mouse wheel.
//
// Each command is a new process, so the pointer's last position is kept per
// tab in the state dir: a pointer that teleports between commands is a tell
// of its own.

type point struct{ X, Y float64 }

// humanEnabled reports whether this command uses humanlike input:
// RODNEY_HUMAN=1/0 decides when set, else the session's --human/--stealth.
func humanEnabled(s *State) bool {
	switch strings.ToLower(os.Getenv("RODNEY_HUMAN")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return s != nil && s.Human
}

// humanTimeout is how much longer than the default a humanlike command may
// take to type text (typing is slow on purpose; the page timeout is not).
func humanTimeout(text string) time.Duration {
	n := len([]rune(text))
	if n > pasteThreshold {
		n = 0
	}
	return defaultTimeout + time.Duration(n)*450*time.Millisecond
}

// --- pointer position, kept between commands ---

var targetIDRE = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)

// pointerFile is where the pointer position of page's tab is kept, or "" if
// its target id isn't a plain token (it becomes a file name).
func pointerFile(page *rod.Page) string {
	id := string(page.TargetID)
	if !targetIDRE.MatchString(id) {
		return ""
	}
	return filepath.Join(stateDir(), "pointer", id)
}

func loadPointer(page *rod.Page) (point, bool) {
	path := pointerFile(page)
	if path == "" {
		return point{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return point{}, false
	}
	var p point
	if _, err := fmt.Sscanf(string(data), "%g %g", &p.X, &p.Y); err != nil {
		return point{}, false
	}
	return p, true
}

func savePointer(page *rod.Page, p point) {
	path := pointerFile(page)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(fmt.Sprintf("%g %g", p.X, p.Y)), 0o600)
}

// viewport returns the page's layout viewport size in CSS pixels.
func viewport(page *rod.Page) (w, h float64, err error) {
	m, err := proto.PageGetLayoutMetrics{}.Call(page)
	if err != nil {
		return 0, 0, err
	}
	return float64(m.CSSLayoutViewport.ClientWidth), float64(m.CSSLayoutViewport.ClientHeight), nil
}

// pointerStart is where the pointer is before a move: where the last command
// left it, kept inside the viewport, else a random spot in its middle.
func pointerStart(page *rod.Page) point {
	w, h, err := viewport(page)
	if err != nil || w < 1 || h < 1 {
		w, h = 1280, 800
	}
	if p, ok := loadPointer(page); ok {
		return point{clamp(p.X, 0, w-1), clamp(p.Y, 0, h-1)}
	}
	return point{w * (0.3 + 0.4*rand.Float64()), h * (0.3 + 0.4*rand.Float64())}
}

// --- paths ---

// fittsSteps is the number of pointer events for a move of distance px onto a
// target width px wide, after ghost-cursor: the Fitts's law index of
// difficulty, plus a random base for the speed of this particular move.
func fittsSteps(distance, width float64) int {
	if width <= 0 {
		width = 100
	}
	id := 2 * math.Log2(distance/width+1)
	steps := int(math.Ceil((math.Log2(id+1) + rand.Float64()*25) * 3))
	return int(clamp(float64(steps), 15, 100))
}

// minimumJerk maps linear time to progress along a reaching movement:
// 10t³ - 15t⁴ + 6t⁵, the velocity profile of human arm movements (Flash and
// Hogan, 1985): it starts and ends at rest instead of at full speed.
func minimumJerk(t float64) float64 {
	return t * t * t * (10 - 15*t + 6*t*t)
}

// bezierPath returns steps+1 points from a to b along a cubic Bézier curve
// whose control points sit at random distances (up to spread px) on the same
// side of the straight line, so the path bows gently as a wrist does.
func bezierPath(a, b point, spread float64, steps int) []point {
	dx, dy := b.X-a.X, b.Y-a.Y
	length := math.Hypot(dx, dy)
	if length < 1 || steps < 1 {
		return []point{a, b}
	}
	nx, ny := -dy/length, dx/length // unit normal to a→b
	side := 1.0
	if rand.IntN(2) == 0 {
		side = -1
	}
	anchor := func() point {
		t := rand.Float64()
		off := side * spread * rand.Float64()
		return point{a.X + dx*t + nx*off, a.Y + dy*t + ny*off}
	}
	c1, c2 := anchor(), anchor()
	// Order the control points along the direction of travel, so the curve
	// doesn't loop back on itself.
	if (c1.X-a.X)*dx+(c1.Y-a.Y)*dy > (c2.X-a.X)*dx+(c2.Y-a.Y)*dy {
		c1, c2 = c2, c1
	}
	pts := make([]point, 0, steps+1)
	for i := 0; i <= steps; i++ {
		t := minimumJerk(float64(i) / float64(steps))
		u := 1 - t
		pts = append(pts, point{
			u*u*u*a.X + 3*u*u*t*c1.X + 3*u*t*t*c2.X + t*t*t*b.X,
			u*u*u*a.Y + 3*u*u*t*c1.Y + 3*u*t*t*c2.Y + t*t*t*b.Y,
		})
	}
	return pts
}

// overshootThreshold and overshootRadius are ghost-cursor's: moves longer
// than 500px land up to 120px off target first, then correct.
const (
	overshootThreshold = 500
	overshootRadius    = 120
)

// humanPath is the full pointer path from a to b for a target width px wide.
func humanPath(a, b point, width float64) []point {
	distance := math.Hypot(b.X-a.X, b.Y-a.Y)
	spread := clamp(distance, 2, 200)
	if distance <= overshootThreshold {
		return bezierPath(a, b, spread, fittsSteps(distance, width))
	}
	angle := rand.Float64() * 2 * math.Pi
	r := overshootRadius * math.Sqrt(rand.Float64())
	over := point{b.X + r*math.Cos(angle), b.Y + r*math.Sin(angle)}
	first := bezierPath(a, over, spread, fittsSteps(distance, width))
	correction := bezierPath(over, b, 10, fittsSteps(r, width)/2+5)
	return append(first, correction[1:]...)
}

// --- dispatch ---

func dispatchMouse(page *rod.Page, ev proto.InputDispatchMouseEvent) error {
	ev.X, ev.Y = math.Round(ev.X), math.Round(ev.Y)
	if ev.PointerType == "" {
		ev.PointerType = proto.InputDispatchMouseEventPointerTypeMouse
	}
	return ev.Call(page)
}

// humanMove moves the pointer from where it is to to, along a humanlike path.
func humanMove(page *rod.Page, to point, width float64) error {
	from := pointerStart(page)
	for i, p := range humanPath(from, to, width) {
		if i == 0 {
			continue
		}
		if err := dispatchMouse(page, proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseMoved, X: p.X, Y: p.Y}); err != nil {
			return err
		}
		// A mouse reports at 60-125Hz; Chrome delivers moves once per frame.
		sleepRange(7, 17)
	}
	savePointer(page, to)
	return nil
}

// elementBox returns the element's border box in viewport coordinates (main
// frame, CSS pixels), the space Input.dispatchMouseEvent works in.
func elementBox(el *rod.Element) (*proto.DOMRect, error) {
	shape, err := el.Shape()
	if err != nil {
		return nil, err
	}
	box := shape.Box()
	if box == nil || box.Width <= 0 || box.Height <= 0 {
		return nil, fmt.Errorf("element has no visible box")
	}
	return box, nil
}

// pointIn is a random point in box, normally distributed around its centre and
// kept off its outer 10%, the way people aim at buttons rather than borders.
func pointIn(box *proto.DOMRect) point {
	off := func() float64 { return clamp(rand.NormFloat64()/6, -0.4, 0.4) }
	return point{box.X + box.Width*(0.5+off()), box.Y + box.Height*(0.5+off())}
}

// humanScrollIntoView brings el into the middle of the viewport with wheel
// notches at the pointer, as a person scrolls, and falls back to a plain
// scrollIntoView when the wheel doesn't move it (an element inside a box that
// scrolls on its own, or a page that eats wheel events).
func humanScrollIntoView(page *rod.Page, el *rod.Element) error {
	for round := 0; round < 4; round++ {
		box, err := elementBox(el)
		if err != nil {
			return el.ScrollIntoView()
		}
		_, h, err := viewport(page)
		if err != nil {
			return err
		}
		if box.Y >= 0 && box.Y+box.Height <= h {
			return nil
		}
		before := box.Y
		if err := humanWheel(page, box.Y+box.Height/2-h/2); err != nil {
			return err
		}
		time.Sleep(250 * time.Millisecond) // let smooth scrolling settle
		if box, err := elementBox(el); err != nil || math.Abs(box.Y-before) < 1 {
			return el.ScrollIntoView()
		}
	}
	return el.ScrollIntoView()
}

// humanWheel scrolls by dy CSS pixels in wheel notches of about 100px (what a
// mouse wheel sends per detent), a short pause between notches.
func humanWheel(page *rod.Page, dy float64) error {
	at := pointerStart(page)
	sign := 1.0
	if dy < 0 {
		sign, dy = -1, -dy
	}
	for dy > 0 {
		notch := math.Min(dy, 100+float64(rand.IntN(21)-10))
		ev := proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseWheel, X: at.X, Y: at.Y, DeltaY: sign * notch}
		if err := dispatchMouse(page, ev); err != nil {
			return err
		}
		dy -= notch
		sleepRange(35, 110)
	}
	savePointer(page, at)
	return nil
}

// humanClick scrolls el into view, moves the pointer onto it, hesitates, and
// presses and releases the left button with a human hold time.
func humanClick(page *rod.Page, el *rod.Element) error {
	at, err := humanPointAt(page, el)
	if err != nil {
		return err
	}
	sleepRange(60, 220)
	left := proto.InputMouseButtonLeft
	if err := dispatchMouse(page, proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMousePressed, X: at.X, Y: at.Y, Button: left, Buttons: gson.Int(1), ClickCount: 1}); err != nil {
		return err
	}
	sleepRange(45, 130)
	return dispatchMouse(page, proto.InputDispatchMouseEvent{Type: proto.InputDispatchMouseEventTypeMouseReleased, X: at.X, Y: at.Y, Button: left, Buttons: gson.Int(0), ClickCount: 1})
}

// humanPointAt scrolls el into view and moves the pointer to a random point on
// it. The point is one where el is actually hit: rod's Click only checks the
// centre and gives up when an overlay covers it, where a person clicks the
// part they can see. When no sampled point hits el, it refuses with rod's
// reason (covered by what, or no pointer events).
func humanPointAt(page *rod.Page, el *rod.Element) (point, error) {
	if err := humanScrollIntoView(page, el); err != nil {
		return point{}, err
	}
	box, err := elementBox(el)
	if err != nil {
		return point{}, err
	}
	at, ok := visiblePointIn(el, box)
	if !ok {
		if _, err := el.Interactable(); err != nil {
			return point{}, err
		}
		at = pointIn(box)
	}
	if err := humanMove(page, at, math.Min(box.Width, box.Height)); err != nil {
		return point{}, err
	}
	return at, nil
}

// visiblePointIn samples points in box until one hits el (or a descendant),
// as elementFromPoint sees it.
func visiblePointIn(el *rod.Element, box *proto.DOMRect) (point, bool) {
	for i := 0; i < 12; i++ {
		p := pointIn(box)
		if i > 6 { // the centre-weighted spread missed: try the whole box
			p = point{box.X + box.Width*(0.1+0.8*rand.Float64()), box.Y + box.Height*(0.1+0.8*rand.Float64())}
		}
		res, err := el.Eval(`function (x, y) {
			if (getComputedStyle(this).pointerEvents === 'none') return false
			const hit = document.elementFromPoint(x, y)
			return hit === this || this.contains(hit)
		}`, math.Round(p.X), math.Round(p.Y))
		if err != nil {
			return point{}, false
		}
		if res.Value.Bool() {
			return p, true
		}
	}
	return point{}, false
}

// --- keyboard ---

// pasteThreshold is the length past which text is pasted rather than typed:
// nobody types a page of text into a form.
const pasteThreshold = 160

// shiftedASCII are the US-layout characters typed with Shift held.
const shiftedASCII = `~!@#$%^&*()_+{}|:"<>?`

// namedKey is a key event spelled out in full. rod's Enter and Tab report
// KeyboardEvent.key as "\r" and "\t" where a keyboard sends "Enter" and "Tab",
// which pages that listen for key === "Enter" (Google's search box) ignore,
// and which no real keyboard produces.
type namedKey struct {
	key, code, text string
	keyCode         int
}

var namedKeys = map[rune]namedKey{
	'\n': {"Enter", "Enter", "\r", 13},
	'\t': {"Tab", "Tab", "", 9},
}

// pressNamed presses and releases k with a typist's dwell time.
func pressNamed(page *rod.Page, k namedKey) error {
	down := proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeRawKeyDown, Key: k.key, Code: k.code, WindowsVirtualKeyCode: k.keyCode, NativeVirtualKeyCode: k.keyCode}
	if k.text != "" {
		down.Type, down.Text, down.UnmodifiedText = proto.InputDispatchKeyEventTypeKeyDown, k.text, k.text
	}
	if err := down.Call(page); err != nil {
		return err
	}
	sleepRange(35, 110)
	return proto.InputDispatchKeyEvent{Type: proto.InputDispatchKeyEventTypeKeyUp, Key: k.key, Code: k.code, WindowsVirtualKeyCode: k.keyCode, NativeVirtualKeyCode: k.keyCode}.Call(page)
}

// keyFor returns rod's key for r, if r is a printable character on a US
// keyboard.
func keyFor(r rune) (key input.Key, ok bool) {
	if r > unicode.MaxASCII || r < ' ' {
		return 0, false
	}
	defer func() {
		if recover() != nil {
			key, ok = 0, false
		}
	}()
	key = input.Key(r)
	_ = key.Info() // panics for keys rod doesn't know
	return key, true
}

// humanType types text into the focused element one key at a time: Shift held
// for capitals and symbols, each key down for a typist's dwell time, and the
// gaps between keys drawn from a log-normal distribution, longer after word
// and sentence breaks. Characters not on the keyboard go in as text, as an
// input method would send them; text longer than pasteThreshold is pasted.
func humanType(page *rod.Page, text string) error {
	runes := []rune(text)
	if len(runes) > pasteThreshold {
		return page.InsertText(text)
	}
	kb := page.Keyboard
	for i, r := range runes {
		key, ok := keyFor(r)
		if named, isNamed := namedKeys[r]; isNamed {
			if err := pressNamed(page, named); err != nil {
				return err
			}
		} else if !ok {
			if err := page.InsertText(string(r)); err != nil {
				return err
			}
		} else {
			shifted := (r < unicode.MaxASCII && unicode.IsUpper(r)) || strings.ContainsRune(shiftedASCII, r)
			if shifted {
				if err := kb.Press(input.ShiftLeft); err != nil {
					return err
				}
				sleepRange(25, 70)
			}
			if err := kb.Press(key); err != nil {
				return err
			}
			sleepRange(35, 110)
			if err := kb.Release(key); err != nil {
				return err
			}
			if shifted {
				sleepRange(10, 40)
				if err := kb.Release(input.ShiftLeft); err != nil {
					return err
				}
			}
		}
		if i < len(runes)-1 {
			time.Sleep(keyGap(r))
		}
	}
	return nil
}

// keyGap is the pause after typing r: log-normal around 120ms, plus a beat
// after spaces and a longer one after punctuation, and now and then a pause
// to think.
func keyGap(r rune) time.Duration {
	ms := clamp(math.Exp(math.Log(120)+0.4*rand.NormFloat64()), 40, 450)
	switch {
	case strings.ContainsRune(".,;:!?\n", r):
		ms += 150 + 250*rand.Float64()
	case r == ' ' && rand.Float64() < 0.3:
		ms += 150 * rand.Float64()
	}
	if rand.Float64() < 0.02 {
		ms += 400 + 800*rand.Float64()
	}
	return time.Duration(ms) * time.Millisecond
}

// humanKey presses and releases a single named key (Backspace, Enter, ...).
func humanKey(page *rod.Page, key input.Key) error {
	if err := page.Keyboard.Press(key); err != nil {
		return err
	}
	sleepRange(35, 110)
	return page.Keyboard.Release(key)
}

// --- scroll command ---

const scrollUsage = "usage: rodney scroll <down|up> [PIXELS] | rodney scroll <selector>"

// parseScrollArgs returns the signed pixel amount for down/up, or the selector
// to bring into view.
func parseScrollArgs(args []string) (dy float64, selector string, err error) {
	if len(args) < 1 || len(args) > 2 {
		return 0, "", fmt.Errorf("%s", scrollUsage)
	}
	switch args[0] {
	case "down", "up":
		px := 600.0
		if len(args) == 2 {
			if px, err = strconv.ParseFloat(args[1], 64); err != nil || px <= 0 {
				return 0, "", fmt.Errorf("invalid pixel amount %q\n%s", args[1], scrollUsage)
			}
		}
		if args[0] == "up" {
			px = -px
		}
		return px, "", nil
	}
	if len(args) == 2 {
		return 0, "", fmt.Errorf("%s", scrollUsage)
	}
	return 0, args[0], nil
}

func cmdScroll(args []string) {
	dy, selector, err := parseScrollArgs(args)
	if err != nil {
		fatal("%v", err)
	}
	s, _, page := withPage()
	human := humanEnabled(s)
	if selector != "" {
		el, err := page.Element(selector)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if human {
			err = humanScrollIntoView(page, el)
		} else {
			err = el.ScrollIntoView()
		}
		if err != nil {
			fatal("scroll failed: %v", err)
		}
		fmt.Println("Scrolled")
		return
	}
	if human {
		err = humanWheel(page, dy)
	} else {
		_, err = page.Eval(`dy => window.scrollBy(0, dy)`, dy)
	}
	if err != nil {
		fatal("scroll failed: %v", err)
	}
	fmt.Println("Scrolled")
}

// --- helpers ---

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

// sleepRange sleeps a uniformly random time between lo and hi milliseconds.
func sleepRange(lo, hi int) {
	time.Sleep(time.Duration(lo+rand.IntN(hi-lo+1)) * time.Millisecond)
}
