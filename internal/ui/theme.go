package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// currentTheme holds the active theme mode: "dark", "light", or "system".
var currentTheme = "dark"

// SetCurrentTheme updates the global theme mode.
func SetCurrentTheme(mode string) {
	currentTheme = mode
}

// GetCurrentTheme returns the active theme mode.
func GetCurrentTheme() string {
	return currentTheme
}

// onThemeChanged is called when the user changes theme in settings.
var onThemeChanged func(mode string)

// SetThemeChangedCallback sets the function to call on theme change.
func SetThemeChangedCallback(fn func(mode string)) {
	onThemeChanged = fn
}

// NotifyThemeChanged triggers the theme change callback.
func NotifyThemeChanged(mode string) {
	currentTheme = mode
	if onThemeChanged != nil {
		onThemeChanged(mode)
	}
}

// erudittoTheme is a custom Fyne theme supporting dark/light modes.
type erudittoTheme struct {
	base fyne.Theme
}

// NewErudittoTheme creates a theme that reads from GetCurrentTheme().
func NewErudittoTheme(getMode func() string) fyne.Theme {
	return &erudittoTheme{base: theme.DefaultTheme()}
}

func (t *erudittoTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	mode := GetCurrentTheme()

	var effective fyne.ThemeVariant
	switch mode {
	case "light":
		effective = theme.VariantLight
	case "dark":
		effective = theme.VariantDark
	default: // "system"
		effective = variant
	}

	// Custom colors for our app
	switch name {
	case theme.ColorNameBackground:
		if effective == theme.VariantLight {
			return color.RGBA{245, 245, 245, 255}
		}
		return color.RGBA{30, 30, 30, 255}
	case theme.ColorNameForeground:
		if effective == theme.VariantLight {
			return color.RGBA{30, 30, 30, 255}
		}
		return color.RGBA{235, 235, 235, 255}
	case theme.ColorNameInputBackground:
		if effective == theme.VariantLight {
			return color.RGBA{255, 255, 255, 255}
		}
		return color.RGBA{45, 45, 45, 255}
	case theme.ColorNameButton:
		if effective == theme.VariantLight {
			return color.RGBA{220, 220, 220, 255}
		}
		return color.RGBA{60, 60, 60, 255}
	case theme.ColorNameHover:
		if effective == theme.VariantLight {
			return color.RGBA{220, 230, 245, 255}
		}
		return color.RGBA{55, 75, 105, 255}
	case theme.ColorNameDisabled:
		if effective == theme.VariantLight {
			return color.RGBA{150, 150, 150, 255}
		}
		return color.RGBA{120, 120, 120, 255}
	case theme.ColorNamePrimary:
		if effective == theme.VariantLight {
			return color.RGBA{R: 0, G: 255, B: 255, A: 64}
		}
		return color.RGBA{R: 0, G: 255, B: 255, A: 64}
	}

	return t.base.Color(name, effective)
}

func (t *erudittoTheme) Font(style fyne.TextStyle) fyne.Resource {
	return t.base.Font(style)
}

func (t *erudittoTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return t.base.Icon(name)
}

func (t *erudittoTheme) Size(name fyne.ThemeSizeName) float32 {
	return t.base.Size(name)
}
