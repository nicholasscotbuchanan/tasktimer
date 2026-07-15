package ui

import (
	"fmt"
	"image/color"

	"fyne.io/fyne/v2"
)

// The icons are Lucide-style: 24x24, no fill, a 2px round-capped stroke.
//
// They are built as static resources with a literal stroke colour rather than
// as theme.ThemedResource, because Fyne's SVG colouriser only ever rewrites the
// `fill` attribute — and it skips any element with `fill="none"` outright. A
// stroke-drawn icon handed to NewThemedResource therefore comes back completely
// unchanged. Since the palette is fixed, baking the colour in is both simpler
// and exact: where an icon needs to appear in two colours (a nav item is muted
// until it is selected, then white), it is simply built twice.
//
// The trailing colour argument is what every constructor below takes, so the
// call site reads as "this glyph, in this colour".

// strokeIcon wraps a Lucide path body in an SVG shell stroked with clr.
func strokeIcon(name, body string, clr color.Color) fyne.Resource {
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" `+
		`viewBox="0 0 24 24" fill="none" stroke="%s" stroke-width="2" `+
		`stroke-linecap="round" stroke-linejoin="round">%s</svg>`, hexString(clr), body)

	// The name must vary with the colour: Fyne caches rasterised resources by
	// name, so two colours sharing one name would render as whichever was
	// drawn first.
	return fyne.NewStaticResource(fmt.Sprintf("%s-%s.svg", name, hexString(clr)[1:]), []byte(svg))
}

// hexString renders a colour as "#rrggbb", dropping alpha — SVG strokes here
// are always opaque.
func hexString(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// Sidebar and branding.

func iconClock(c color.Color) fyne.Resource {
	return strokeIcon("clock", `<circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/>`, c)
}

func iconDashboard(c color.Color) fyne.Resource {
	return strokeIcon("dashboard",
		`<rect x="3" y="3" width="7" height="7" rx="1"/><rect x="14" y="3" width="7" height="7" rx="1"/>`+
			`<rect x="14" y="14" width="7" height="7" rx="1"/><rect x="3" y="14" width="7" height="7" rx="1"/>`, c)
}

func iconList(c color.Color) fyne.Resource {
	return strokeIcon("list",
		`<line x1="8" y1="6" x2="21" y2="6"/><line x1="8" y1="12" x2="21" y2="12"/>`+
			`<line x1="8" y1="18" x2="21" y2="18"/><line x1="3" y1="6" x2="3.01" y2="6"/>`+
			`<line x1="3" y1="12" x2="3.01" y2="12"/><line x1="3" y1="18" x2="3.01" y2="18"/>`, c)
}

func iconReports(c color.Color) fyne.Resource {
	return strokeIcon("reports",
		`<rect x="9" y="9" width="13" height="13" rx="2"/>`+
			`<path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/>`, c)
}

func iconSettings(c color.Color) fyne.Resource {
	return strokeIcon("settings",
		`<circle cx="12" cy="12" r="3"/>`+
			`<path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 `+
			`1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 `+
			`1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 `+
			`1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/>`, c)
}

func iconInfo(c color.Color) fyne.Resource {
	return strokeIcon("info",
		`<circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/>`+
			`<line x1="12" y1="8" x2="12.01" y2="8"/>`, c)
}

// Timer controls.

func iconPlay(c color.Color) fyne.Resource {
	return strokeIcon("play", `<polygon points="5 3 19 12 5 21 5 3"/>`, c)
}

func iconStop(c color.Color) fyne.Resource {
	return strokeIcon("stop", `<rect x="6" y="6" width="12" height="12" rx="2"/>`, c)
}

func iconFlag(c color.Color) fyne.Resource {
	return strokeIcon("flag",
		`<path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z"/>`+
			`<line x1="4" y1="22" x2="4" y2="15"/>`, c)
}

func iconReset(c color.Color) fyne.Resource {
	return strokeIcon("reset",
		`<path d="M3 2v6h6"/><path d="M3.51 9a9 9 0 1 0 2.13-3.36L3 8"/>`, c)
}

// Table and toolbar.

func iconRefresh(c color.Color) fyne.Resource {
	return strokeIcon("refresh",
		`<path d="M23 4v6h-6"/><path d="M1 20v-6h6"/>`+
			`<path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10"/>`+
			`<path d="M20.49 15a9 9 0 0 1-14.85 3.36L1 14"/>`, c)
}

func iconSearch(c color.Color) fyne.Resource {
	return strokeIcon("search",
		`<circle cx="11" cy="11" r="8"/><line x1="21" y1="21" x2="16.65" y2="16.65"/>`, c)
}

func iconCheck(c color.Color) fyne.Resource {
	return strokeIcon("check", `<polyline points="20 6 9 17 4 12"/>`, c)
}

func iconPlus(c color.Color) fyne.Resource {
	return strokeIcon("plus",
		`<line x1="12" y1="5" x2="12" y2="19"/><line x1="5" y1="12" x2="19" y2="12"/>`, c)
}

func iconChevronDown(c color.Color) fyne.Resource {
	return strokeIcon("chevron-down", `<polyline points="6 9 12 15 18 9"/>`, c)
}

func iconExternal(c color.Color) fyne.Resource {
	return strokeIcon("external",
		`<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`+
			`<polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/>`, c)
}

func iconUser(c color.Color) fyne.Resource {
	return strokeIcon("user",
		`<path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2"/><circle cx="12" cy="7" r="4"/>`, c)
}
