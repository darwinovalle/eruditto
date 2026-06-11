// Package ui implements the Fyne windows for Eruditto.
package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/history"
	"github.com/darwinovalle/eruditto/internal/hotkeys"
	"github.com/darwinovalle/eruditto/internal/settings"
)

// SettingsWindow is the preferences editor.
type SettingsWindow struct {
	app             fyne.App
	win             fyne.Window
	settingsSvc     *settings.Service
	hotkeyMgr       hotkeys.HotkeyManager
	repo            *history.Repository
	dataDir         string
	onHotkeyChanged func(newShortcut hotkeys.Shortcut) error
	built           bool
}

func NewSettingsWindow(
	app fyne.App,
	settingsSvc *settings.Service,
	hotkeyMgr hotkeys.HotkeyManager,
	repo *history.Repository,
	dataDir string,
	onHotkeyChanged func(newShortcut hotkeys.Shortcut) error,
) *SettingsWindow {
	if app == nil {
		panic("ui: SettingsWindow requires a non-nil fyne.App")
	}
	if settingsSvc == nil {
		panic("ui: SettingsWindow requires a non-nil settings.Service")
	}
	return &SettingsWindow{
		app:             app,
		settingsSvc:     settingsSvc,
		hotkeyMgr:       hotkeyMgr,
		repo:            repo,
		dataDir:         dataDir,
		onHotkeyChanged: onHotkeyChanged,
	}
}

func (s *SettingsWindow) Show() {
	if !s.built {
		s.buildWindow()
	}
	s.loadValues()
	s.win.Show()
	s.win.RequestFocus()
}

func (s *SettingsWindow) Hide() {
	if s.win != nil {
		s.win.Hide()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Window construction — Clean, modern form layout
// ─────────────────────────────────────────────────────────────────────────────

type settingsForm struct {
	hotkeyEntry     *widget.Entry
	hotkeyErrLabel  *widget.Label
	maxHistoryEntry *widget.Entry
	themeSelect     *widget.Select
	bootCheck       *widget.Check
	pollEntry       *widget.Entry
	statsLabel      *widget.Label
	autoPasteCheck  *widget.Check
}

var sf settingsForm

func (s *SettingsWindow) buildWindow() {
	s.win = s.app.NewWindow("Settings")
	s.win.Resize(fyne.NewSize(480, 520))
	s.win.CenterOnScreen()
	s.win.SetFixedSize(true)

	// ── Hotkey field ──────────────────────────────────────────────────
	sf.hotkeyEntry = widget.NewEntry()
	sf.hotkeyEntry.SetPlaceHolder("ctrl+shift+v")
	sf.hotkeyErrLabel = widget.NewLabelWithStyle(
		"", fyne.TextAlignLeading, fyne.TextStyle{},
	)
	sf.hotkeyErrLabel.Hide()

	sf.hotkeyEntry.OnChanged = func(val string) {
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" {
			sf.hotkeyErrLabel.Hide()
			return
		}
		if err := domain.ValidateSetting(domain.KeyHotkey, val); err != nil {
			sf.hotkeyErrLabel.SetText("⚠ " + friendlyHotkeyError(err.Error()))
			sf.hotkeyErrLabel.Show()
		} else {
			sf.hotkeyErrLabel.Hide()
		}
	}

	hotkeySection := container.NewVBox(
		sf.hotkeyEntry,
		sf.hotkeyErrLabel,
		widget.NewLabelWithStyle(
			"Format: ctrl+shift+v · Modifiers: ctrl, shift, alt, super",
			fyne.TextAlignLeading,
			fyne.TextStyle{Italic: true},
		),
	)

	// ── Other fields ──────────────────────────────────────────────────
	sf.maxHistoryEntry = widget.NewEntry()
	sf.maxHistoryEntry.SetPlaceHolder("5000")

	sf.themeSelect = widget.NewSelect([]string{"dark", "light", "system"}, nil)

	sf.bootCheck = widget.NewCheck("Launch Eruditto at login", nil)

	sf.autoPasteCheck = widget.NewCheck(
		"Paste immediately after selecting a clip",
		nil,
	)

	sf.pollEntry = widget.NewEntry()
	sf.pollEntry.SetPlaceHolder("100")

	sf.statsLabel = widget.NewLabelWithStyle(
		"…", fyne.TextAlignLeading, fyne.TextStyle{Italic: true},
	)
	go s.loadStats()

	// ── Danger zone ───────────────────────────────────────────────────
	clearBtn := widget.NewButton("Clear all history…", func() {
		dialog.ShowConfirm(
			"Clear all clipboard history",
			"This permanently deletes all clips that are not pinned.\n"+
				"Favorites are preserved. This cannot be undone.",
			func(confirmed bool) {
				if confirmed {
					s.clearHistory()
				}
			},
			s.win,
		)
	})
	clearBtn.Importance = widget.LowImportance

	openDataDirBtn := widget.NewButton("Open data directory", func() {
		s.openDataDir()
	})
	openDataDirBtn.Importance = widget.LowImportance

	// ── Action buttons ────────────────────────────────────────────────
	cancelBtn := widget.NewButton("Cancel", func() { s.Hide() })
	saveBtn := widget.NewButton("Save", func() { s.save() })
	saveBtn.Importance = widget.HighImportance

	// ── Form layout — Clean aligned labels like mockup ─────────────────
	form := widget.NewForm(
		widget.NewFormItem("Shortcut", hotkeySection),
		widget.NewFormItem("Max history", container.NewVBox(
			sf.maxHistoryEntry,
			widget.NewLabelWithStyle(
				"Oldest clips are deleted when this limit is reached.",
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true},
			),
		)),
		widget.NewFormItem("Theme", sf.themeSelect),
		widget.NewFormItem("Start on boot", sf.bootCheck),
		widget.NewFormItem(
			"Auto paste",
			container.NewVBox(
				sf.autoPasteCheck,
				widget.NewLabelWithStyle(
					"When enabled, selecting a clip automatically pastes it "+
						"into the previously focused application.",
					fyne.TextAlignLeading,
					fyne.TextStyle{Italic: true},
				),
			),
		),
		widget.NewFormItem("Poll interval (ms)", container.NewVBox(
			sf.pollEntry,
			widget.NewLabelWithStyle(
				"How often the clipboard is checked. Minimum is 100 ms.",
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true},
			),
		)),
	)

	// History section
	statsSection := container.NewVBox(
		widget.NewSeparator(),
		widget.NewLabelWithStyle(
			"History", fyne.TextAlignLeading, fyne.TextStyle{Bold: true},
		),
		sf.statsLabel,
		container.NewHBox(clearBtn, layout.NewSpacer(), openDataDirBtn),
	)

	actionRow := container.NewHBox(layout.NewSpacer(), cancelBtn, saveBtn)

	content := container.NewVBox(
		form,
		statsSection,
		widget.NewSeparator(),
		actionRow,
	)

	s.win.SetContent(container.NewPadded(content))
	s.built = true
}

// ─────────────────────────────────────────────────────────────────────────────
// Load / Save
// ─────────────────────────────────────────────────────────────────────────────

func (s *SettingsWindow) loadValues() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	all, err := s.settingsSvc.GetAll(ctx)
	if err != nil {
		return
	}

	sf.hotkeyEntry.SetText(all[domain.KeyHotkey])
	sf.maxHistoryEntry.SetText(all[domain.KeyMaxHistory])
	sf.themeSelect.SetSelected(all[domain.KeyTheme])
	sf.bootCheck.SetChecked(all[domain.KeyStartOnBoot] == "true")
	sf.pollEntry.SetText(all[domain.KeyPollIntervalMs])
	sf.autoPasteCheck.SetChecked(all[domain.KeyAutoPaste] == "true")

	go s.loadStats()
}

func (s *SettingsWindow) save() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hotkeyStr := strings.TrimSpace(strings.ToLower(sf.hotkeyEntry.Text))
	maxHistory := strings.TrimSpace(sf.maxHistoryEntry.Text)
	selectedTheme := sf.themeSelect.Selected
	bootStr := "false"
	if sf.bootCheck.Checked {
		bootStr = "true"
	}
	pollMs := strings.TrimSpace(sf.pollEntry.Text)
	autoPaste := "false"
	if sf.autoPasteCheck.Checked {
		autoPaste = "true"
	}

	type field struct{ key, val string }
	fields := []field{
		{domain.KeyHotkey, hotkeyStr},
		{domain.KeyMaxHistory, maxHistory},
		{domain.KeyTheme, selectedTheme},
		{domain.KeyStartOnBoot, bootStr},
		{domain.KeyPollIntervalMs, pollMs},
		{domain.KeyAutoPaste, autoPaste},
	}

	for _, f := range fields {
		if err := domain.ValidateSetting(f.key, f.val); err != nil {
			dialog.ShowError(
				fmt.Errorf("invalid value for %q: %w", f.key, err),
				s.win,
			)
			return
		}
	}

	for _, f := range fields {
		if err := s.settingsSvc.Set(ctx, f.key, f.val); err != nil {
			dialog.ShowError(err, s.win)
			return
		}
	}

	if s.onHotkeyChanged != nil {
		oldStr, _ := s.settingsSvc.Get(ctx, domain.KeyHotkey)
		if oldStr != hotkeyStr {
			newShortcut, err := hotkeys.ParseShortcut(hotkeyStr)
			if err != nil {
				dialog.ShowError(
					fmt.Errorf("hotkey %q is not valid: %w", hotkeyStr, err),
					s.win,
				)
				return
			}

			if err := s.onHotkeyChanged(newShortcut); err != nil {
				dialog.ShowError(
					fmt.Errorf("hotkey %q could not be registered: %w\n"+
						"Other settings were saved.", hotkeyStr, err),
					s.win,
				)
			}
		}
	}

	s.Hide()
}

// ─────────────────────────────────────────────────────────────────────────────
// Stats, clear, open data dir
// ─────────────────────────────────────────────────────────────────────────────

func (s *SettingsWindow) loadStats() {
	if s.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	clips, err := s.repo.Recent(ctx, 100_000)
	if err != nil {
		sf.statsLabel.SetText("Could not load statistics.")
		return
	}

	var textCount, imageCount, favCount int
	for _, c := range clips {
		switch c.Type {
		case domain.ClipTypeText:
			textCount++
		case domain.ClipTypeImage:
			imageCount++
		}
		if c.IsFavorite {
			favCount++
		}
	}

	var parts []string
	if textCount > 0 {
		parts = append(parts, fmt.Sprintf("%s text", formatInt(textCount)))
	}
	if imageCount > 0 {
		label := fmt.Sprintf("%s image", formatInt(imageCount))
		if imageCount != 1 {
			label += "s"
		}
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		sf.statsLabel.SetText("No history yet.")
		return
	}

	summary := strings.Join(parts, ", ")
	if favCount > 0 {
		summary += fmt.Sprintf(" · %s pinned", formatInt(favCount))
	}
	sf.statsLabel.SetText(summary)
}

func (s *SettingsWindow) clearHistory() {
	if s.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	total, err := s.repo.Count(ctx)
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}

	clips, err := s.repo.Recent(ctx, int(total))
	if err != nil {
		dialog.ShowError(err, s.win)
		return
	}

	var favCount int64
	for _, c := range clips {
		if c.IsFavorite {
			favCount++
		}
	}

	if _, err := s.repo.EnforceMaxHistory(ctx, int(favCount)); err != nil {
		dialog.ShowError(err, s.win)
		return
	}

	go s.loadStats()
	dialog.ShowInformation(
		"History cleared",
		"All non-pinned clips have been deleted.",
		s.win,
	)
}

func (s *SettingsWindow) openDataDir() {
	dir := s.dataDir
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = home + "/.local/share/eruditto"
	}
	cmd := exec.Command("xdg-open", dir)
	if err := cmd.Start(); err != nil {
		dialog.ShowError(
			fmt.Errorf("could not open file manager: %w\nPath: %s", err, dir),
			s.win,
		)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func friendlyHotkeyError(raw string) string {
	for _, prefix := range []string{
		"settings: invalid value: ",
		"settings: set \"hotkey\"=",
	} {
		if idx := strings.Index(raw, prefix); idx >= 0 {
			raw = raw[idx+len(prefix):]
		}
	}
	if len(raw) > 0 && raw[0] == '"' {
		if end := strings.Index(raw[1:], `": `); end >= 0 {
			raw = raw[end+4:]
		}
	}
	if len(raw) > 0 {
		raw = strings.ToUpper(raw[:1]) + raw[1:]
	}
	const maxLen = 80
	if len(raw) > maxLen {
		raw = raw[:maxLen] + "…"
	}
	return raw
}
