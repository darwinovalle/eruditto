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
	case theme.ColorNameInputBorder:
		// The Fyne default light-mode input border is #e3e3e3, which
		// blends into our white input/checkbox background and makes
		// unchecked checkmarks invisible. A medium grey outline keeps
		// the checkbox box clearly visible in both themes.
		if effective == theme.VariantLight {
			return color.RGBA{158, 158, 158, 255}
		}
		return color.RGBA{90, 90, 90, 255}
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
		// A solid green (not the old washed-out translucent cyan) so
		// confirm dialogs, settings Save, and checkmarks read clearly
		// as "affirmative" actions in both light and dark themes.
		if effective == theme.VariantLight {
			return color.RGBA{46, 125, 50, 255}
		}
		return color.RGBA{102, 187, 106, 255}
	case theme.ColorNameForegroundOnPrimary:
		// Text/icons drawn on top of the green primary button. White
		// gives good contrast against the greens chosen above.
		return color.RGBA{255, 255, 255, 255}
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
