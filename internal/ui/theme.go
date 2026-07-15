// Package ui builds the Task Timer desktop interface: a custom Fyne theme, a
// small set of shared components, and the five pages behind the sidebar.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"

	"task-timer-app/internal/assets"
)

// The palette. One place to change the look; nothing below defines a colour
// inline. Names describe the role, not the hue, so a future light variant can
// swap the values without renaming every use site.
var (
	colPage      = hex(0x0D1522) // content area behind the cards
	colSidebar   = hex(0x0A0F1A) // nav rail, a shade below the page
	colCard      = hex(0x131C2C) // card and table surfaces
	colCardAlt   = hex(0x182233) // table header strip, raised rows
	colBorder    = hex(0x1F2A3E) // hairline around cards and inputs
	colInput     = hex(0x111A28) // entry and select interiors
	colHover     = hex(0x1E2A3F) // pointer over an interactive surface
	colText      = hex(0xE8EEF7) // primary copy
	colTextMuted = hex(0x8494A9) // labels, column headers, secondary copy
	colTextDim   = hex(0x5D6B80) // placeholders and disabled copy

	colPrimary  = hex(0x2F6BEB) // buttons, active nav, focus
	colAccent   = hex(0x5B9CFF) // the one figure worth reading twice
	colSuccess  = hex(0x22C55E) // online dot, healthy states
	colDanger   = hex(0xEF4444) // stop, destructive actions
	colAvatar   = hex(0x4F46E5) // user chip
	colPillText = hex(0x5FD68F) // status pill copy
	colPillFill = rgba(0x22C55E, 0x2E)
)

// Radii and metrics the components share with the theme, so a button rendered
// by Fyne and a card drawn by hand round off by the same amount.
const (
	radiusCard    float32 = 14
	radiusControl float32 = 8
	radiusPill    float32 = 11
	radiusNav     float32 = 10

	gutter float32 = 20 // space between cards and page edges
)

// The text faces. Fyne's default theme renders in NotoSans and ships Inter only
// as the raw material for its symbol font, so the family is vendored in
// internal/assets and installed here.
var (
	fontRegular    = fyne.NewStaticResource("Inter-Regular.ttf", assets.InterRegular)
	fontBold       = fyne.NewStaticResource("Inter-Bold.ttf", assets.InterBold)
	fontItalic     = fyne.NewStaticResource("Inter-Italic.ttf", assets.InterItalic)
	fontBoldItalic = fyne.NewStaticResource("Inter-BoldItalic.ttf", assets.InterBoldItalic)
)

// taskTimerTheme restyles Fyne's stock widgets to the app's palette and sets the
// app's typeface. Icons still come from the default theme, which is where the
// stock widgets — the Select's chevron, the scrollbars — get theirs.
type taskTimerTheme struct{}

var _ fyne.Theme = taskTimerTheme{}

// Color maps Fyne's semantic colour names onto the palette above. The variant
// is ignored: the app is dark in both, because the design is.
func (taskTimerTheme) Color(name fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameBackground:
		return colPage
	case theme.ColorNameForeground, theme.ColorNameForegroundOnPrimary:
		return colText
	case theme.ColorNamePrimary:
		return colPrimary

	// Secondary buttons — Lap, Reset, Refresh — are the plain button colour.
	// The primary-tinted ones opt in with widget.HighImportance.
	case theme.ColorNameButton:
		return colCardAlt
	case theme.ColorNameDisabledButton:
		return hex(0x151D2B)
	case theme.ColorNameDisabled:
		return colTextDim
	case theme.ColorNameHover:
		return colHover
	case theme.ColorNamePressed:
		return rgba(0xFFFFFF, 0x14)

	case theme.ColorNameInputBackground:
		return colInput
	case theme.ColorNameInputBorder, theme.ColorNameSeparator:
		return colBorder
	case theme.ColorNamePlaceHolder:
		return colTextDim

	case theme.ColorNameFocus:
		return rgba(0x2F6BEB, 0x66)
	case theme.ColorNameSelection:
		return rgba(0x2F6BEB, 0x40)

	case theme.ColorNameSuccess:
		return colSuccess
	case theme.ColorNameError:
		return colDanger
	case theme.ColorNameWarning:
		return hex(0xF59E0B)

	// Menus and dialogs float above the page, so they take the card surface
	// rather than the page's — otherwise they vanish into the background.
	case theme.ColorNameOverlayBackground, theme.ColorNameMenuBackground:
		return colCard
	case theme.ColorNameHeaderBackground:
		return colCardAlt
	case theme.ColorNameScrollBar:
		return rgba(0xFFFFFF, 0x24)
	case theme.ColorNameShadow:
		return rgba(0x000000, 0x66)

	default:
		return theme.DefaultTheme().Color(name, theme.VariantDark)
	}
}

// Font maps a text style onto one of the vendored Inter faces.
//
// Monospace and Symbol deliberately fall through to the default theme: Inter
// has no monospaced cut, and Fyne's symbol face is itself derived from Inter, so
// it already matches.
func (taskTimerTheme) Font(style fyne.TextStyle) fyne.Resource {
	switch {
	case style.Monospace, style.Symbol:
		return theme.DefaultTheme().Font(style)
	case style.Bold && style.Italic:
		return fontBoldItalic
	case style.Bold:
		return fontBold
	case style.Italic:
		return fontItalic
	default:
		return fontRegular
	}
}

func (taskTimerTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (taskTimerTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNameText:
		return 13
	case theme.SizeNameHeadingText:
		return 20
	case theme.SizeNameSubHeadingText:
		return 15
	case theme.SizeNameCaptionText:
		return 11
	case theme.SizeNamePadding:
		return 5
	case theme.SizeNameInnerPadding:
		return 10
	case theme.SizeNameInputBorder:
		return 1
	case theme.SizeNameSeparatorThickness:
		return 1
	case theme.SizeNameInputRadius, theme.SizeNameSelectionRadius:
		return radiusControl
	case theme.SizeNameScrollBarSmall:
		return 4
	case theme.SizeNameScrollBar:
		return 10
	default:
		return theme.DefaultTheme().Size(name)
	}
}

// hex expands 0xRRGGBB to an opaque colour.
func hex(v uint32) color.NRGBA {
	return color.NRGBA{
		R: uint8(v >> 16),
		G: uint8(v >> 8),
		B: uint8(v),
		A: 0xFF,
	}
}

// rgba expands 0xRRGGBB plus an explicit alpha.
func rgba(v uint32, alpha uint8) color.NRGBA {
	c := hex(v)
	c.A = alpha
	return c
}
