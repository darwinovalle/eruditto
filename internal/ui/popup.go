// Package ui implements the Fyne windows for Eruditto.
//
// Two windows exist:
//   - PopupWindow  — the clipboard history picker (this file)
//   - SettingsWindow — preferences editor (settings.go)
//
// Design constraints:
//   - Open in under 100ms perceived latency: clips are loaded in a
//     goroutine; the window appears immediately with a loading state.
//   - Close on focus loss: standard Ditto-style behavior.
//   - No global state: both windows are constructed once at startup
//     and shown/hidden on demand. Construction is cheap; showing is
//     the hot path.
package ui

import (
	"context"
	"fmt"
	"image/color"
	"log/slog"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/darwinovalle/eruditto/internal/clipboard"
	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/history"
	"github.com/darwinovalle/eruditto/internal/hotkeys"
	"github.com/darwinovalle/eruditto/internal/settings"
)

// popupPageSize is the number of clips loaded per query.
const popupPageSize = 200

// previewMaxRunes is the character limit for the text preview in each row.
const previewMaxRunes = 15

// popupWidth is the fixed width of the popup window.
const popupWidth = 300

// popupHeight is the fixed height of the popup window.
const popupHeight = 400

// PopupWindow is the clipboard history picker.
type PopupWindow struct {
	app      fyne.App
	win      fyne.Window
	clipSvc  *clipboard.Service
	repo     *history.Repository
	settingsSvc *settings.Service

	// pasteHotkey holds the global hotkey that opens the popup
	// (e.g. ctrl+shift+z). pasteClip temporarily unregisters it
	// before sending the synthetic paste keypress so that the
	// keypress injection does not race against the hotkey grab
	// and re-open the popup in the middle of the paste. See
	// pasteClip's doc for details.
	//
	// Optional — when nil, no guard is performed (the paste runs
	// with the hotkey still active). Callers that own a
	// HotkeyManager should set these fields via the
	// setPasteHotkey constructor or by direct assignment right
	// after NewPopupWindow.
	pasteHotkeyHotkeyMgr hotkeys.HotkeyManager
	pasteHotkeyShortcut  hotkeys.Shortcut
	pasteHotkeyHandler   func()

	allClips []domain.Clip
	filtered []domain.Clip

	searchEntry *widget.Entry
	clipList    *widget.List
	countLabel  *widget.Label
	statusLabel *widget.Label

	clipChanged chan struct{}

	built bool

	// previousWindowID is the X11 window ID of the application
	// that had focus immediately before the popup opened. We
	// capture it in Show() and restore it explicitly before
	// sending the auto-paste keypress in pasteClip().
	//
	// Without this, the window manager may take a variable
	// amount of time (or fail entirely on some compositors) to
	// return focus to the previously-focused window after the
	// popup hides, and the auto-paste ctrl+v is swallowed.
	previousWindowID string

	// rowCache maps list row containers to their metadata.
	rowCache map[fyne.CanvasObject]*clipRow
}

// NewPopupWindow constructs the popup. The window is hidden until Show().
func NewPopupWindow(
	app fyne.App,
	clipSvc *clipboard.Service,
	repo *history.Repository,
) *PopupWindow {
	if app == nil {
		panic("ui: PopupWindow requires a non-nil fyne.App")
	}
	if clipSvc == nil {
		panic("ui: PopupWindow requires a non-nil clipboard.Service")
	}
	if repo == nil {
		panic("ui: PopupWindow requires a non-nil history.Repository")
	}
	return &PopupWindow{
		app:         app,
		clipSvc:     clipSvc,
		repo:        repo,
		clipChanged: make(chan struct{}, 1),
		rowCache:    make(map[fyne.CanvasObject]*clipRow),
	}
}

// SetPasteHotkeyHook wires the global hotkey that opens the popup
// into the paste-suppression guard. Calling this makes pasteClip
// temporarily Unregister the hotkey before sending the synthetic
// paste keypress, then re-register it 300ms later. This works
// around an X11 race where synthesised key events from xdotool
// fire the global hotkey grab and re-open the popup mid-paste.
//
// Both mgr and handler are required (mgr=nil, handler=nil
// disable the guard for backward compatibility, but pasting into
// terminals/TUIs without calling this may re-fire the popup).
func (p *PopupWindow) SetPasteHotkeyHook(mgr hotkeys.HotkeyManager, sc hotkeys.Shortcut, handler func()) {
	if mgr == nil || handler == nil {
		p.pasteHotkeyHotkeyMgr = nil
		p.pasteHotkeyShortcut = hotkeys.Shortcut{}
		p.pasteHotkeyHandler = nil
		return
	}
	p.pasteHotkeyHotkeyMgr = mgr
	p.pasteHotkeyShortcut = sc
	p.pasteHotkeyHandler = handler
}

// SetSettingsService wires the settings service into the popup so
// showAndPosition can read the user's "popup follows mouse"
// preference and choose between cursor-near positioning and
// screen-centered positioning. Optional — when nil, the popup
// stays in cursor-near mode (the original behaviour).
func (p *PopupWindow) SetSettingsService(svc *settings.Service) {
	p.settingsSvc = svc
}

// readMouseTrackingSetting returns the user's preference for
// whether the popup follows the mouse cursor. Default is true
// (follow mouse, the historical behaviour) when the setting
// can't be read.
//
// Called from showAndPosition on the Fyne thread; the lookup
// is short-lived so it's safe to read synchronously here.
func (p *PopupWindow) readMouseTrackingSetting() bool {
	if p.settingsSvc == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	val, err := p.settingsSvc.Get(ctx, domain.KeyPopupMouseTracking)
	if err != nil {
		return true
	}
	return val == "true"
}

// NotifyClipChanged signals the popup that clipboard history has changed.
func (p *PopupWindow) NotifyClipChanged() {
	select {
	case p.clipChanged <- struct{}{}:
	default:
	}
}

// StartListening starts the background goroutine that watches for clip changes.
func (p *PopupWindow) StartListening(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.clipChanged:
				fyne.Do(func() {
					if p.built && p.win.Content() != nil {
						go p.loadClips(p.currentQuery())
					}
				})
			}
		}
	}()
}

func (p *PopupWindow) currentQuery() string {
	if p.searchEntry == nil {
		return ""
	}
	return p.searchEntry.Text
}

// Show opens the popup and reloads clips fresh from the DB.
// Positions the window near the mouse cursor on the active screen.
func (p *PopupWindow) Show() {
	// Capture the currently-focused X11 window BEFORE the popup
	// steals focus. We use this ID later in pasteClip to explicitly
	// reactivate the previous window before sending ctrl+v for
	// auto-paste. We do not block on the call: if xdotool fails
	// (no display server, no focused window), we leave the field
	// empty and pasteClip falls back to the time-based sleep.
	if id, err := clipboard.CaptureActiveWindowID(); err == nil {
		p.previousWindowID = id
	}

	fyne.Do(func() {
		if !p.built {
			p.build()
		}
		p.searchEntry.SetText("")
		p.setStatus("Loading...")

		p.showAndPosition()
		go p.loadClips("")
	})
}

// Hide closes the popup.
func (p *PopupWindow) Hide() {
	if p.win != nil {
		p.win.Hide()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Window construction
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) build() {
	p.win = p.app.NewWindow("Eruditto")
	p.win.Resize(fyne.NewSize(popupWidth, popupHeight))
	p.win.SetFixedSize(true)

	// Close on focus loss
	p.win.SetOnClosed(func() {})
	p.win.Canvas().SetOnTypedKey(p.handleKey)

	// ── Search entry ──────────────────────────────────────────────────
	p.searchEntry = widget.NewEntry()
	p.searchEntry.SetPlaceHolder("Search clipboard...")
	p.searchEntry.OnChanged = p.onSearchChanged

	searchContainer := container.NewPadded(p.searchEntry)

	// ── Clip list ─────────────────────────────────────────────────────
	p.clipList = widget.NewList(
		func() int { return len(p.filtered) },
		p.createRow,
		p.updateRow,
	)
	p.clipList.OnSelected = p.onClipSelected

	// ── Footer ────────────────────────────────────────────────────────
	p.countLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{})
	p.statusLabel = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Italic: true})

	hint := widget.NewLabelWithStyle(
		" esc close",
		fyne.TextAlignTrailing,
		fyne.TextStyle{Monospace: true},
	)

	footer := container.NewHBox(p.countLabel, layout.NewSpacer(), hint)

	// ── Layout ────────────────────────────────────────────────────────
	content := container.NewBorder(
		container.NewVBox(
			searchContainer,
			widget.NewSeparator(),
		),
		container.NewVBox(
			widget.NewSeparator(),
			container.NewPadded(footer),
		),
		nil, nil,
		p.clipList,
	)

	// Wrap content in a fixed-size container to prevent any child from
	// expanding the window beyond our desired dimensions.
	fixedContent := container.NewGridWrap(fyne.NewSize(popupWidth, popupHeight), content)

	p.win.SetContent(fixedContent)
	p.built = true
}

// ─────────────────────────────────────────────────────────────────────────────
// Custom pin icon resource (pushpin SVG)
// ─────────────────────────────────────────────────────────────────────────────

var pinIconOutlined = fyne.NewStaticResource("pin_outlined.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15"viewBox="0 0 24 24" fill="none" stroke="#8b8b8b" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-pin-icon lucide-pin"><path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/></svg>`))
var pinIconFilled = fyne.NewStaticResource("pin_filled.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="#8b8b8b" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="lucide lucide-pin-off-icon lucide-pin-off"><path d="M12 17v5"/><path d="M15 9.34V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H7.89"/><path d="m2 2 20 20"/><path d="M9 9v1.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h11"/></svg>`))

// pinIcon returns the appropriate pin icon based on pinned state.
func pinIcon(pinned bool) fyne.Resource {
	if pinned {
		return pinIconFilled
	}
	return pinIconOutlined
}

// ─────────────────────────────────────────────────────────────────────────────
// Tight layout with zero spacing
// ─────────────────────────────────────────────────────────────────────────────

// tightVBox is a layout that stacks items vertically with zero padding.
type tightVBox struct{}

func (t *tightVBox) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		h := obj.MinSize().Height
		obj.Resize(fyne.NewSize(size.Width, h))
		obj.Move(fyne.NewPos(0, y))
		y += h
	}
}

func (t *tightVBox) MinSize(objects []fyne.CanvasObject) fyne.Size {
	h := float32(0)
	w := float32(0)
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		min := obj.MinSize()
		h += min.Height
		if min.Width > w {
			w = min.Width
		}
	}
	return fyne.NewSize(w, h)
}

// ─────────────────────────────────────────────────────────────────────────────
// List row construction — Ultra-compact with zero-gap layout
// ─────────────────────────────────────────────────────────────────────────────

type clipRow struct {
	container    *fyne.Container
	bgRect       *canvas.Rectangle
	previewLabel *widget.Label
	timeLabel    *widget.Label
	pinBtn       *widget.Button
	deleteBtn    *widget.Button
	index        int
	clipID       int64
}

func (p *PopupWindow) createRow() fyne.CanvasObject {
	// Background rectangle for pinned highlight
	bgRect := canvas.NewRectangle(nil)

	// Preview label - main content, single line
	previewLabel := widget.NewLabel("")
	previewLabel.Truncation = fyne.TextTruncateEllipsis
	previewLabel.Wrapping = fyne.TextWrapOff

	// Time label - small, subtle
	timeLabel := widget.NewLabelWithStyle("", fyne.TextAlignLeading,
		fyne.TextStyle{Italic: true})

	// Pin button
	pinBtn := widget.NewButtonWithIcon("", pinIcon(false), nil)
	pinBtn.Importance = widget.LowImportance

	// Delete button
	deleteBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	deleteBtn.Importance = widget.LowImportance

	// Right side: icons horizontal
	rightSide := container.NewHBox(pinBtn, deleteBtn)

	// Main content: preview + time with ZERO gap using custom layout
	leftContent := container.New(&tightVBox{}, previewLabel, timeLabel)

	// Full row: left content + right icons
	rowContent := container.NewBorder(nil, nil, nil, rightSide, leftContent)

	// Background + content
	rowContainer := container.NewMax(bgRect, rowContent)

	row := &clipRow{
		container:    rowContainer,
		bgRect:       bgRect,
		previewLabel: previewLabel,
		timeLabel:    timeLabel,
		pinBtn:       pinBtn,
		deleteBtn:    deleteBtn,
	}

	p.rowCache[rowContainer] = row
	return rowContainer
}

func (p *PopupWindow) updateRow(id widget.ListItemID, obj fyne.CanvasObject) {
	if int(id) >= len(p.filtered) {
		return
	}
	clip := p.filtered[id]

	row, ok := p.rowCache[obj]
	if !ok {
		return
	}

	// Update preview text
	if clip.Type == domain.ClipTypeImage {
		row.previewLabel.SetText("[image]")
	} else {
		row.previewLabel.SetText(clip.DisplayContent(previewMaxRunes))
	}

	// Update time
	row.timeLabel.SetText(relativeTime(clip.CreatedAt))

	// Update pin icon appearance
	row.pinBtn.SetIcon(pinIcon(clip.IsFavorite))

	// Update button handlers
	clipID := clip.ID
	clipIdx := id
	row.clipID = clipID
	row.index = int(clipIdx)

	row.pinBtn.OnTapped = func() { p.toggleFavorite(clipID, int(clipIdx)) }
	row.deleteBtn.OnTapped = func() { p.confirmDelete(clipID, int(clipIdx)) }

	// Background: cyan when pinned (from theme), transparent when not
	if clip.IsFavorite {
		row.bgRect.FillColor = theme.Color(theme.ColorNamePrimary)
	} else {
		row.bgRect.FillColor = color.Transparent
	}
	row.bgRect.Refresh()
}

// ─────────────────────────────────────────────────────────────────────────────
// Data loading — Pinned clips always stay at the top
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) loadClips(query string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var (
		clips []domain.Clip
		err   error
	)

	if strings.TrimSpace(query) == "" {
		clips, err = p.repo.Recent(ctx, popupPageSize)
	} else {
		clips, err = p.repo.Search(ctx, query, popupPageSize)
	}

	fyne.Do(func() {
		if err != nil {
			p.setStatus("Failed to load clips: " + err.Error())
			return
		}

		// Sort: pinned clips first, then by creation time (newest first)
		sort.Slice(clips, func(i, j int) bool {
			if clips[i].IsFavorite != clips[j].IsFavorite {
				return clips[i].IsFavorite // true comes first
			}
			return clips[i].CreatedAt.After(clips[j].CreatedAt)
		})

		p.allClips = clips
		p.filtered = clips

		p.refreshList()

		if len(clips) == 0 {
			if strings.TrimSpace(query) != "" {
				p.setStatus(fmt.Sprintf("No results for %q", query))
			} else {
				p.setStatus("No clipboard history yet. Copy something!")
			}
		} else {
			p.setStatus("")
		}

		p.updateCountLabel()
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Search — Pinned clips stay at top even when filtering
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) onSearchChanged(query string) {
	if strings.TrimSpace(query) == "" {
		p.filtered = p.allClips
		p.refreshList()
		p.setStatus("")
		p.updateCountLabel()
		return
	}

	go p.loadClips(query)
}

// ─────────────────────────────────────────────────────────────────────────────
// Selection & actions
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) onClipSelected(id widget.ListItemID) {
	if id >= len(p.filtered) {
		return
	}
	clip := p.filtered[id]
	p.pasteClip(clip)
}

func (p *PopupWindow) pasteClip(clip domain.Clip) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := p.clipSvc.RestoreClip(ctx, clip); err != nil {
		dialog.ShowError(err, p.win)
		return
	}

	p.Hide()

	if !p.clipSvc.IsAutoPasteEnabled(ctx) {
		return
	}

	slog.Debug("pasteClip: auto-paste path entered",
		"clip_type", clip.Type.String(),
		"clip_id", clip.ID,
		"previous_window_id", p.previousWindowID,
	)

	go func() {
		// Explicitly reactivate the window that had focus before
		// the popup opened. windowactivate --sync blocks until the
		// window manager confirms the focus change, so by the time
		// it returns, the target window is guaranteed to be the
		// active one. The 50ms sleep is a small extra cushion for
		// the WM to finish any compositor-side transitions.
		//
		// If we failed to capture the window ID at Show() time
		// (empty previousWindowID), fall back to a longer sleep so
		// the popup's Hide() has time to release focus on its own.
		if p.previousWindowID != "" {
			slog.Debug("pasteClip: activating previous window",
				"window_id", p.previousWindowID,
			)
			if err := clipboard.ActivateWindow(p.previousWindowID); err != nil {
				slog.Warn("pasteClip: activate previous window failed",
					"window_id", p.previousWindowID,
					"error", err,
				)
				p.app.SendNotification(&fyne.Notification{
					Title:   "Eruditto",
					Content: "auto-paste: could not reactivate previous window: " + err.Error(),
				})
				time.Sleep(150 * time.Millisecond)
			} else {
				slog.Debug("pasteClip: previous window activated, sleeping 50ms")
				time.Sleep(50 * time.Millisecond)
			}
		} else {
			slog.Warn("pasteClip: previous window id empty, falling back to 150ms sleep")
			time.Sleep(150 * time.Millisecond)
		}

		slog.Debug("pasteClip: sending paste shortcut",
			"target_window_id", p.previousWindowID,
			"clip_type", clip.Type.String(),
		)
		isImage := clip.Type == domain.ClipTypeImage

		// Temporarily unregister the global hotkey that opens
		// this popup while we send the synthetic paste keypress.
		//
		// Why: xdotool's synthetic key events have been observed
		// (and reproduced in production logging) to fire the
		// global hotkey grab for ctrl+shift+z while the
		// keypress injection is in flight, which re-opens the
		// popup and steals focus from the target window before
		// the paste lands — most visibly when the target is a
		// terminal/TUI where the paste never gets a chance to
		// reach the inner application.
		//
		// The guard unregisters the hotkey before AutoPaste,
		// gives the X server ~20ms to release the grab, sends
		// the paste, then re-registers the hotkey after a
		// generous 300ms grace period. The re-registration
		// happens in a goroutine to keep pasteClip from
		// blocking.
		if p.pasteHotkeyHotkeyMgr != nil && p.pasteHotkeyHandler != nil {
			if err := p.pasteHotkeyHotkeyMgr.Unregister(p.pasteHotkeyShortcut); err != nil {
				slog.Warn("pasteClip: failed to suspend hotkey",
					"shortcut", p.pasteHotkeyShortcut.Raw,
					"error", err,
				)
			} else {
				slog.Debug("pasteClip: suspended popup hotkey")
				time.Sleep(20 * time.Millisecond)
				defer func() {
					go func() {
						time.Sleep(300 * time.Millisecond)
						if err := p.pasteHotkeyHotkeyMgr.Register(p.pasteHotkeyShortcut, p.pasteHotkeyHandler); err != nil {
							slog.Warn("pasteClip: failed to restore hotkey",
								"shortcut", p.pasteHotkeyShortcut.Raw,
								"error", err,
							)
						} else {
							slog.Debug("pasteClip: restored popup hotkey")
						}
					}()
				}()
			}
		}

		if err := AutoPaste(p.previousWindowID, isImage); err != nil {
			slog.Warn("pasteClip: AutoPaste failed", "error", err)
			p.app.SendNotification(&fyne.Notification{
				Title:   "Eruditto",
				Content: err.Error(),
			})
		}
	}()
}

func (p *PopupWindow) toggleFavorite(clipID int64, idx int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	newVal, err := p.repo.ToggleFavorite(ctx, clipID)
	if err != nil {
		dialog.ShowError(err, p.win)
		return
	}

	// Update in-memory slices
	for i := range p.allClips {
		if p.allClips[i].ID == clipID {
			p.allClips[i].IsFavorite = newVal
			break
		}
	}
	for i := range p.filtered {
		if p.filtered[i].ID == clipID {
			p.filtered[i].IsFavorite = newVal
			break
		}
	}

	// Re-sort to move pinned/unpinned to correct positions
	sort.Slice(p.filtered, func(i, j int) bool {
		if p.filtered[i].IsFavorite != p.filtered[j].IsFavorite {
			return p.filtered[i].IsFavorite
		}
		return p.filtered[i].CreatedAt.After(p.filtered[j].CreatedAt)
	})
	sort.Slice(p.allClips, func(i, j int) bool {
		if p.allClips[i].IsFavorite != p.allClips[j].IsFavorite {
			return p.allClips[i].IsFavorite
		}
		return p.allClips[i].CreatedAt.After(p.allClips[j].CreatedAt)
	})

	p.refreshList()
	p.updateCountLabel()
}

// confirmDelete shows a confirmation dialog before deleting.
func (p *PopupWindow) confirmDelete(clipID int64, idx int) {
	dialog.ShowConfirm(
		"Delete clip",
		"Remove this item from clipboard history? This cannot be undone.",
		func(confirmed bool) {
			if !confirmed {
				return
			}
			p.deleteClip(clipID, idx)
		},
		p.win,
	)
}

// deleteClip removes a clip from the repository and updates the UI.
func (p *PopupWindow) deleteClip(clipID int64, idx int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := p.repo.Delete(ctx, clipID); err != nil {
		dialog.ShowError(err, p.win)
		return
	}

	// Remove from both slices.
	p.allClips = removeByID(p.allClips, clipID)
	if idx < len(p.filtered) {
		p.filtered = append(p.filtered[:idx], p.filtered[idx+1:]...)
	}

	p.refreshList()
	p.updateCountLabel()
}

// ─────────────────────────────────────────────────────────────────────────────
// Keyboard handling
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) handleKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyEscape:
		p.Hide()

	case fyne.KeyReturn, fyne.KeyEnter:
		if len(p.filtered) == 0 {
			return
		}
		idx := 0
		p.pasteClip(p.filtered[idx])

	case fyne.KeyUp:
		p.clipList.ScrollToTop()

	case fyne.KeyDown:
		p.win.Canvas().Focus(p.clipList)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UI state helpers
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) refreshList() {
	p.clipList.UnselectAll()
	p.clipList.Refresh()
	if len(p.filtered) > 0 {
		p.clipList.ScrollToTop()
	}
}

func (p *PopupWindow) setStatus(msg string) {
	p.statusLabel.SetText(msg)
	if msg == "" {
		p.statusLabel.Hide()
	} else {
		p.statusLabel.Show()
	}
}

func (p *PopupWindow) updateCountLabel() {
	total := len(p.allClips)
	favs := 0
	for _, c := range p.allClips {
		if c.IsFavorite {
			favs++
		}
	}

	switch {
	case total == 0:
		p.countLabel.SetText("No clips")
	default:
		// Always show pinned count so footer width never changes
		p.countLabel.SetText(fmt.Sprintf("%s clips · %s pinned",
			formatInt(total), formatInt(favs)))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pure helpers
// ─────────────────────────────────────────────────────────────────────────────

func relativeTime(t time.Time) string {
	diff := time.Since(t)
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		m := int(diff.Minutes())
		if m == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", m)
	case diff < 24*time.Hour:
		h := int(diff.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	case diff < 7*24*time.Hour:
		d := int(diff.Hours() / 24)
		if d == 1 {
			return "yesterday"
		}
		return fmt.Sprintf("%d days ago", d)
	default:
		return t.Format("Jan 2")
	}
}

func formatInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	result := make([]byte, 0, len(s)+len(s)/3)
	offset := len(s) % 3
	if offset == 0 {
		offset = 3
	}
	result = append(result, s[:offset]...)
	for i := offset; i < len(s); i += 3 {
		result = append(result, ',')
		result = append(result, s[i:i+3]...)
	}
	return string(result)
}

func removeByID(clips []domain.Clip, id int64) []domain.Clip {
	out := make([]domain.Clip, 0, len(clips))
	for _, c := range clips {
		if c.ID != id {
			out = append(out, c)
		}
	}
	return out
}
