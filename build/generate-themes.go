// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build ignore

// Generates the Dracula Pro theme CSS files by transplanting each palette in
// build/dracula-pro-palettes.json onto the built-in Gitea light and dark themes.
// The palettes are extracted from https://github.com/sdthach/Dracula-Pro.

package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	themeDir    = "web_src/css/themes"
	paletteFile = "build/dracula-pro-palettes.json"

	// lightness distance from a family's anchor at which the transplant fades to nothing
	decay = 0.35
)

type variant struct {
	Slug    string            `json:"slug"`
	Display string            `json:"display"`
	Colors  map[string]string `json:"colors"`
}

func loadVariants() []variant {
	data, err := os.ReadFile(paletteFile)
	if err != nil {
		panic(err)
	}
	var variants []variant
	if err := json.Unmarshal(data, &variants); err != nil {
		panic(err)
	}
	return variants
}

// families whose members track the reference theme's surface or text anchor
var (
	surfaceVars = strings.Fields(`body box-header box-body box-body-highlight timeline input-background
		input-toggle-background hover-opaque menu card button code-bg shadow shadow-opaque
		secondary-bg expand-button tooltip-bg nav-bg secondary-nav-bg console-bg
		console-border console-hover-bg console-active-bg console-menu-bg console-menu-border
		grey black black-light black-dark-1 black-dark-2 transparency-grid-light
		transparency-grid-dark diff-inactive workflow-edge-hover overlay-backdrop light
		light-border hover active markup-table-row markup-code-block markup-code-inline
		reaction-bg label-bg label-hover-bg label-active-bg`)

	textVars = strings.Fields(`text-dark text text-light text-light-1 text-light-2 text-light-3
		tooltip-text console-fg console-fg-subtle console-link grey-light`)
)

var (
	hues = map[string]string{
		"red": "red", "orange": "orange", "yellow": "yellow", "olive": "olive",
		"green": "green", "teal": "teal", "blue": "blue", "violet": "violet",
		"purple": "magenta_purple", "pink": "pink", "brown": "brown",
	}
	semantic = map[string]string{
		"error": "red", "success": "green", "warning": "yellow",
		"info": "cyan", "priority": "magenta_purple",
	}
	diffHues = map[string]string{"added": "green", "removed": "red", "moved": "editorGutter.modifiedBackground"}
	syntax   = map[string]string{
		"keyword": "pink", "bool": "purple", "control": "pink", "name": "green",
		"type": "cyan", "number": "purple", "operator": "pink", "regexp": "red",
		"string": "yellow", "comment": "comment", "invalid": "red", "tag": "pink",
		"attribute": "green", "property": "cyan", "variable": "fg",
		"string-special": "yellow", "escape": "pink", "entity": "purple",
		"preproc": "pink", "preproc-file": "yellow", "decorator": "green",
		"namespace": "fg", "name-pseudo": "green", "comment-special": "comment",
		"text": "fg", "text-alt": "comment", "punctuation": "fg", "whitespace": "comment",
		"diff-fg": "fg", "deleted-bg": "red", "inserted-bg": "green", "emph": "yellow",
		"strong": "orange", "heading": "purple", "subheading": "cyan", "output": "fg",
		"prompt": "green", "traceback": "red", "matching-bracket-bg": "cyan",
		"nonmatching-bracket-bg": "red",
	}
	ansi = map[string]string{
		"black": "ansiBlack", "red": "red", "green": "green", "yellow": "yellow",
		"blue": "ansiBlue", "magenta": "pink", "cyan": "cyan",
		"bright-black": "ansiBrightBlack", "bright-red": "ansiBrightRed",
		"bright-green": "ansiBrightGreen", "bright-yellow": "ansiBrightYellow",
		"bright-blue": "ansiBrightBlue", "bright-magenta": "ansiBrightMagenta",
		"bright-cyan": "ansiBrightCyan",
	}
	// variables carrying brand or absolute colors, left untouched
	keepVars = map[string]bool{"--color-git": true, "--color-logo": true, "--color-white": true}

	// Gitea variables the VS Code themes state outright, taken from the palette rather than transplanted
	upstream = map[string]string{
		"--color-primary":          "button.background",
		"--color-primary-contrast": "button.foreground",
		"--color-primary-hover":    "button.hoverBackground",
		"--color-label-bg":         "badge.background",
		"--color-label-text":       "badge.foreground",
		"--color-nav-bg":           "statusBar.background",
		"--color-nav-text":         "statusBar.foreground",
		"--color-secondary-nav-bg": "statusBar.border",
		"--color-active":           "list.activeSelectionBackground",
		"--color-text":             "list.activeSelectionForeground",
		"--color-hover":            "list.hoverBackground",
		"--color-error-text":       "errorForeground",
		"--color-red":              "editorError.foreground",
		"--color-yellow":           "editorWarning.foreground",
		"--color-error-border":     "inputValidation.errorBorder",
		"--color-warning-border":   "inputValidation.warningBorder",
		"--color-info-border":      "inputValidation.infoBorder",
		"--color-accent":           "progressBar.background",
	}

	// light-mode surface ramp below the body, deepest last
	surfaceRamp = map[string]float64{"box-body": 0.02, "box-header": 0.04}
)

const hueAlt = `red|orange|yellow|olive|green|teal|blue|violet|purple|pink|brown`

var (
	declRe    = regexp.MustCompile(`^(\s*)(--color-[a-z0-9-]+):\s*([^;]+);(.*)$`)
	hueRe     = regexp.MustCompile(`^(` + hueAlt + `)(-light|-dark-[12])?$`)
	badgeRe   = regexp.MustCompile(`^(red|green|yellow|orange)-badge(-bg|-hover-bg)?$`)
	semRe     = regexp.MustCompile(`^(error|success|warning|info|priority)-(border|bg|bg-active|bg-hover|text)$`)
	diffRe    = regexp.MustCompile(`^diff-(added|removed|moved)-(fg|linenum-bg|row-bg|row-border|word-bg)$`)
	varRe     = regexp.MustCompile(`^var\((--color-[a-z0-9-]+)\)$`)
	displayRe = regexp.MustCompile(`(--theme-display-name:\s*)"[^"]*"`)
	schemeRe  = regexp.MustCompile(`(--theme-color-scheme:\s*)"[^"]*"`)
)

type hsl struct{ h, l, s float64 }

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// mod1 wraps a hue into [0,1); math.Mod alone keeps the sign of a negative hue.
func mod1(v float64) float64 {
	v = math.Mod(v, 1)
	if v < 0 {
		v++
	}
	return v
}

func rgbToHSL(r, g, b float64) hsl {
	maxc, minc := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	sum, diff := maxc+minc, maxc-minc
	l := sum / 2
	if maxc == minc {
		return hsl{0, l, 0}
	}
	var s float64
	if l <= 0.5 {
		s = diff / sum
	} else {
		s = diff / (2 - maxc - minc)
	}
	rc, gc, bc := (maxc-r)/diff, (maxc-g)/diff, (maxc-b)/diff
	var h float64
	switch maxc {
	case r:
		h = bc - gc
	case g:
		h = 2 + rc - bc
	default:
		h = 4 + gc - rc
	}
	return hsl{mod1(h / 6), l, s}
}

func hslToRGB(c hsl) (r, g, b float64) {
	if c.s == 0 {
		return c.l, c.l, c.l
	}
	var m2 float64
	if c.l <= 0.5 {
		m2 = c.l * (1 + c.s)
	} else {
		m2 = c.l + c.s - c.l*c.s
	}
	m1 := 2*c.l - m2
	return channel(m1, m2, c.h+1.0/3), channel(m1, m2, c.h), channel(m1, m2, c.h-1.0/3)
}

func channel(m1, m2, h float64) float64 {
	h = mod1(h)
	switch {
	case h < 1.0/6:
		return m1 + (m2-m1)*h*6
	case h < 0.5:
		return m2
	case h < 2.0/3:
		return m1 + (m2-m1)*(2.0/3-h)*6
	}
	return m1
}

func parseColor(s string) (hsl, string) {
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 || len(h) == 4 {
		var b strings.Builder
		for _, c := range h {
			b.WriteRune(c)
			b.WriteRune(c)
		}
		h = b.String()
	}
	alpha := ""
	if len(h) == 8 {
		alpha = h[6:8]
	}
	var ch [3]float64
	for i := range ch {
		v, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			panic("bad color " + s)
		}
		ch[i] = float64(v) / 255
	}
	return rgbToHSL(ch[0], ch[1], ch[2]), alpha
}

func formatColor(c hsl, alpha string) string {
	r, g, b := hslToRGB(hsl{mod1(c.h), clamp01(c.l), clamp01(c.s)})
	return fmt.Sprintf("#%02x%02x%02x%s", to255(r), to255(g), to255(b), alpha)
}

func to255(v float64) int { return int(math.RoundToEven(v * 255)) }

func at(p map[string]string, key string) string {
	v, ok := p[key]
	if !ok {
		panic("missing palette key " + key)
	}
	return v
}

func mix(a, b string) string {
	ac, _ := parseColor(a)
	bc, _ := parseColor(b)
	if math.Abs(ac.h-bc.h) > 0.5 { // take the short way round the wheel
		if ac.h < bc.h {
			ac.h++
		} else {
			ac.h--
		}
	}
	return formatColor(hsl{(ac.h + bc.h) / 2, (ac.l + bc.l) / 2, (ac.s + bc.s) / 2}, "")
}

func shade(c string, dl, satMul float64) string {
	v, alpha := parseColor(c)
	return formatColor(hsl{v.h, v.l + dl, v.s * satMul}, alpha)
}

// derive fills in the hues Gitea names but a Dracula palette does not carry.
func derive(src map[string]string) map[string]string {
	p := maps.Clone(src)
	if o := p["orange"]; o == "" || o == p["red"] || o == p["yellow"] {
		p["orange"] = mix(at(p, "red"), at(p, "yellow"))
	}
	p["olive"] = mix(at(p, "green"), at(p, "yellow"))
	p["violet"] = at(p, "purple")
	p["magenta_purple"] = mix(at(p, "purple"), at(p, "pink"))
	p["teal"] = at(p, "cyan")
	p["blue"] = mix(at(p, "cyan"), p["violet"])
	p["brown"] = shade(p["orange"], -0.20, 0.55)
	p["gold"] = shade(at(p, "yellow"), -0.16, 0.70)
	return p
}

// classify maps a Gitea color variable to its family, palette key and family anchor.
func classify(name string) (family, key, anchor string, ok bool) {
	s := strings.TrimPrefix(name, "--color-")
	switch {
	case keepVars[name], strings.HasPrefix(s, "series-16-"):
		return "", "", "", false
	case strings.HasPrefix(s, "primary"):
		return "primary", "accent", "--color-primary", true
	case strings.HasPrefix(s, "secondary"):
		return "secondary", "comment", "--color-secondary", true
	}
	for _, v := range surfaceVars {
		if s == v {
			return "surface", "bg", "--color-body", true
		}
	}
	for _, v := range textVars {
		if s == v {
			return "text", "fg", "--color-text-dark", true
		}
	}
	if m := hueRe.FindStringSubmatch(s); m != nil {
		return m[1], hues[m[1]], "--color-" + m[1], true
	}
	if m := badgeRe.FindStringSubmatch(s); m != nil {
		return m[1] + "-badge", hues[m[1]], "--color-" + m[1] + "-badge", true
	}
	if m := semRe.FindStringSubmatch(s); m != nil {
		return m[1], semantic[m[1]], "--color-" + m[1] + "-text", true
	}
	if m := diffRe.FindStringSubmatch(s); m != nil {
		a := "--color-diff-" + m[1] + "-fg"
		if m[1] == "moved" {
			a = "--color-diff-moved-row-border"
		}
		return "diff-" + m[1], diffHues[m[1]], a, true
	}
	if s == "gold" || s == "highlight-fg" || s == "highlight-bg" {
		return "gold", "gold", "--color-gold", true
	}
	return "", "", "", false
}

func readRef(path string) (lines []string, vals map[string]string) {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	lines = strings.Split(string(data), "\n")
	vals = map[string]string{}
	for _, ln := range lines {
		if m := declRe.FindStringSubmatch(ln); m != nil {
			if v := strings.TrimSpace(m[3]); strings.HasPrefix(v, "#") {
				vals[m[2]] = v
			}
		}
	}
	return lines, vals
}

// build rewrites a reference theme's color declarations onto the palette. Each
// variable moves to the palette hue wholesale, while its lightness and saturation
// shift toward the family target weighted by its distance from the family anchor,
// so a family keeps the reference theme's internal contrast steps.
func build(refPath string, pal map[string]string, dark bool) string {
	lines, rv := readRef(refPath)

	target := func(family, key, anchor, short string) string {
		a, _ := parseColor(rv[anchor])
		t, _ := parseColor(at(pal, key))
		switch {
		case family == "secondary":
			return formatColor(hsl{t.h, a.l, math.Min(t.s, 0.30)}, "")
		case dark:
			return formatColor(t, "")
		case family == "surface":
			b, _ := parseColor(at(pal, "bg")) // light surfaces invert the variant's own body
			return formatColor(hsl{b.h, 1 - b.l/2 - surfaceRamp[short], math.Max(b.s, 0.30)}, "")
		case family == "text":
			return formatColor(hsl{t.h, a.l, 0.22}, "")
		case family == "primary":
			return formatColor(hsl{t.h, math.Min(t.l, 0.56), math.Min(t.s, 0.80)}, "")
		}
		return formatColor(hsl{t.h, a.l, math.Min(t.s, 0.75)}, "")
	}

	// value transplants one declaration, or returns "" to keep the reference's own.
	value := func(name, val string) string {
		hex := strings.HasPrefix(val, "#")
		var cur hsl
		var alpha string
		if hex {
			cur, alpha = parseColor(val)
		}
		if key, mapped := upstream[name]; mapped && dark {
			return at(pal, key)[:7] + alpha
		}
		if !hex {
			return ""
		}
		switch {
		case name == "--color-primary-contrast":
			if dark {
				return at(pal, "accent_fg") + alpha
			}
			return "#ffffff" + alpha
		case strings.HasPrefix(name, "--color-ansi-"):
			if k, found := ansi[strings.TrimPrefix(name, "--color-ansi-")]; found {
				return at(pal, k) + alpha
			}
		case strings.HasPrefix(name, "--color-syntax-"):
			if k, found := syntax[strings.TrimPrefix(name, "--color-syntax-")]; found {
				c, ok := pal["syn_"+k]
				if !ok {
					c = at(pal, k)
				}
				if dark {
					return c + alpha
				}
				t, _ := parseColor(c)
				return formatColor(hsl{t.h, cur.l, math.Min(t.s, 0.85)}, alpha)
			}
		default:
			family, key, anchor, ok := classify(name)
			if k, mapped := upstream[name]; mapped {
				key = k
			}
			if ok && rv[anchor] != "" {
				a, _ := parseColor(rv[anchor])
				t, _ := parseColor(target(family, key, anchor, strings.TrimPrefix(name, "--color-")))
				f := math.Max(0, 1-math.Abs(cur.l-a.l)/decay)
				return formatColor(hsl{t.h, cur.l + (t.l-a.l)*f, cur.s + (t.s-a.s)*f}, alpha)
			}
		}
		return ""
	}

	vals := make(map[string]string, len(lines))
	for _, ln := range lines {
		if m := declRe.FindStringSubmatch(ln); m != nil {
			name, val := m[2], strings.TrimSpace(m[3])
			if v := value(name, val); v != "" {
				vals[name] = v
			} else {
				vals[name] = val
			}
		}
	}

	// Gitea's aliases must not survive: point each at what this theme emitted for its target.
	resolve := func(name string) string {
		v := vals[name]
		for range len(vals) {
			m := varRe.FindStringSubmatch(v)
			if m == nil {
				return v
			}
			next, ok := vals[m[1]]
			if !ok {
				return ""
			}
			v = next
		}
		return ""
	}

	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		m := declRe.FindStringSubmatch(ln)
		if m == nil {
			out = append(out, ln)
			continue
		}
		v := resolve(m[2])
		if v == "" {
			out = append(out, ln)
			continue
		}
		out = append(out, m[1]+m[2]+": "+v+";"+m[4])
	}
	return strings.Join(out, "\n")
}

func setMeta(css, display, scheme string) string {
	css = displayRe.ReplaceAllString(css, `${1}"`+display+`"`)
	return schemeRe.ReplaceAllString(css, `${1}"`+scheme+`"`)
}

func write(name, content string) {
	if err := os.WriteFile(filepath.Join(themeDir, name), []byte(content), 0o644); err != nil {
		panic(err)
	}
}

func main() {
	variants := loadVariants()
	for _, v := range variants {
		pal := derive(v.Colors)
		for _, scheme := range []string{"dark", "light"} {
			css := build(filepath.Join(themeDir, "theme-gitea-"+scheme+".css"), pal, scheme == "dark")
			display := v.Display + " " + strings.ToUpper(scheme[:1]) + scheme[1:]
			write("theme-"+v.Slug+"-"+scheme+".css", setMeta(css, display, scheme))
		}
		write("theme-"+v.Slug+"-auto.css", fmt.Sprintf(
			"@import \"./theme-%[1]s-light.css\" (prefers-color-scheme: light);\n"+
				"@import \"./theme-%[1]s-dark.css\" (prefers-color-scheme: dark);\n\n"+
				"gitea-theme-meta-info {\n  --theme-display-name: \"%[2]s Auto\";\n"+
				"  --theme-color-scheme: \"auto\";\n}\n", v.Slug, v.Display))
	}
	fmt.Printf("generated %d variants (%d files)\n", len(variants), len(variants)*3)
}
