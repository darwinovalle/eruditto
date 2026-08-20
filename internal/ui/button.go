// Package ui implements the Fyne windows for Eruditto.
package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// paletteTheme wraps the active app theme and overrides a handful of color
// names for a single widget, so buttons can carry their own colors without
// restyling the whole UI. A nil field falls back to the underlying theme.
type paletteTheme struct {
	base  fyne.Theme
	bg    color.Color // theme.ColorNameButton     (solid button background)
	fg    color.Color // theme.ColorNameForeground (button text)
	hover color.Color // theme.ColorNameHover      (hover tint, blended over bg)
}

func (t *paletteTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNameButton:
		if t.bg != nil {
			return t.bg
		}
	case theme.ColorNameForeground:
		if t.fg != nil {
			return t.fg
		}
	case theme.ColorNameHover:
		if t.hover != nil {
			return t.hover
		}
	}
	return t.base.Color(name, variant)
}

func (t *paletteTheme) Font(style fyne.TextStyle) fyne.Resource { return t.base.Font(style) }
func (t *paletteTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}
func (t *paletteTheme) Size(name fyne.ThemeSizeName) float32 { return t.base.Size(name) }

// newSolidButton returns a button with a solid custom background and text
// color. The hover color is alpha-blended over the background by Fyne, so it
// must be translucent — an opaque hover would fully replace the background
// (which is what washed the green Save button out to white).
func newSolidButton(label string, bg, fg, hover color.Color, onTapped func()) fyne.CanvasObject {
	btn := widget.NewButton(label, onTapped)
	btn.Importance = widget.MediumImportance
	p := &paletteTheme{base: theme.Current(), bg: bg, fg: fg, hover: hover}
	return container.NewThemeOverride(btn, p)
}

// withButtonHover re-themes only the hover color of an existing button, keeping
// the colors its importance drives (e.g. a HighImportance green Save button).
func withButtonHover(btn *widget.Button, hover color.Color) fyne.CanvasObject {
	p := &paletteTheme{base: theme.Current(), hover: hover}
	return container.NewThemeOverride(btn, p)
}

// newOutlinedButton returns a "ghost" button: a transparent body, the theme's
// normal text color, and a 1px border. hover should be translucent so it tints
// the body without hiding the border.
func newOutlinedButton(label string, border, hover color.Color, onTapped func()) fyne.CanvasObject {
	btn := widget.NewButton(label, onTapped)
	btn.Importance = widget.MediumImportance
	p := &paletteTheme{base: theme.Current(), bg: color.Transparent, hover: hover}

	outline := canvas.NewRectangle(color.Transparent)
	outline.StrokeColor = border
	outline.StrokeWidth = 1
	outline.CornerRadius = theme.Size(theme.SizeNameInputRadius)

	return container.NewMax(outline, container.NewThemeOverride(btn, p))
}
