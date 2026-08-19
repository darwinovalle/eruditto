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

// searchBarHeight is the fixed height of the search bar strip in
// pixels. Keeping it constant (rather than letting the text's own
// line height drive it) makes the bar's vertical position stable and
// pixel-exact across fonts and text sizes.
const searchBarHeight = 26

// searchBarTextNudgeY is a small vertical offset (px) added on top of
// geometric centering. Most fonts reserve more space above the cap
// height than below the baseline, so a tiny positive value makes the
// glyphs read as optically centered even though the line box is
// centered. Tune this to taste.
const searchBarTextNudgeY = 1

// PopupWindow is the clipboard history picker.
type PopupWindow struct {
	app         fyne.App
	win         fyne.Window
	clipSvc     *clipboard.Service
	repo        *history.Repository
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

	// searchMode is true while the user has invoked slash-command
	// search by pressing "/". While true, printable characters are
	// appended to query and the list is filtered against it. Esc
	// exits search mode (without hiding the popup); characters
	// typed are dropped outside this mode.
	//
	// The traditional persistent-search-entry widget was a trap:
	// it claimed keyboard focus and Fyne's "focused-widget first"
	// dispatch silently dropped Esc/Enter because the entry
	// doesn't handle Escape and our canvas-level OnTypedKey only
	// fires when nothing is focused. Slash-mode keeps the entry
	// out of the focus tree entirely — key events flow through
	// the canvas-level handler at all times.
	searchMode bool
	// suppressSlashRune is set to true for one rune cycle after
	// handleKey detects KeySlash. The slash's character is also
	// delivered as a TypedRune ('/' = 0x2F), but the user
	// pressed the same physical key both events refer to and
	// only wants to enter search mode, not type a slash literal.
	// We suppress exactly one rune then re-clear the flag so
	// subsequent '/' characters fall into the query as normal
	// (e.g. a query like "http://example.com" still works).
	suppressSlashRune bool
	// query is the slice of characters typed after "/". Used to
	// filter allClips into filtered.
	query string
	// searchBar is the text at the top of the popup that displays
	// "/<query>" while in search mode, or a grey "type to search..."
	// placeholder when the query is empty. Hidden otherwise.
	//
	// We use canvas.Text (not widget.Label) so the placeholder can be
	// rendered in a dimmed colour — widget.Label only supports the
	// theme foreground colour.
	searchBar *canvas.Text
	// searchArea is the container holding the search bar plus its
	// divider line. Toggling visibility on the whole container (and
	// refreshing it) reliably re-lays-out the popup, which a bare
	// label Show()/Hide() does not always do in Fyne — otherwise the
	// bar stays visually collapsed until some other refresh happens.
	searchArea  *fyne.Container
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

	// selectedID tracks the row currently highlighted by the
	// popup list. We keep our own copy because fyne.widget.List
	// does not expose a public SelectedID() method, but OnSelected
	// fires on every Select() call (including programmatic) so
	// we hook it in onClipSelected to keep this in sync.
	selectedID widget.ListItemID

	// navigating is set to true while programmatic Select() calls
	// happen from handleArrow / handleEnter. onClipSelected checks
	// it to suppress the auto-paste that would otherwise fire
	// every time the list re-highlights an arrow-key target.
	navigating bool
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
		settingsSvc: nil,
		// -1 means "no row selected yet"; handleArrow treats it
		// as "start from the top" so the first j/k move lands
		// the user on the natural first row instead of jumping
		// straight to a previously-pasted row.
		selectedID:  -1,
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

// readVimNavigationSetting returns the user's preference for
// vim-style j/k navigation in the popup. Default is false
// (arrow-key-only) when the setting can't be read.
func (p *PopupWindow) readVimNavigationSetting() bool {
	if p.settingsSvc == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	val, err := p.settingsSvc.Get(ctx, domain.KeyVimNavigation)
	if err != nil {
		return false
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
	return p.query
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
		// Return focus to canvas so arrow / j-k keys fire immediately
		p.win.Canvas().Focus(nil)
		// Reset slash-search state so a fresh popup opens
		// in plain list mode, never inheriting a prior query.
		p.exitSearchMode(false)
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

	// Close-button semantics: clicking the popup's title-bar X
	// hides the window instead of quitting the application.
	// Eruditto is a tray-resident daemon — explicit quit lives
	// in the system tray's "Quit Eruditto" menu item. Without
	// the close intercept Fyne treats the click like Quit on
	// most platforms, which kicks the user out of the daemon
	// even though the monitor / clipboard / hotkey services
	// are still wanted in the background.
	//
	// We also set SetOnClosed to a no-op so that, if anything
	// ever does trigger an actual Close() (e.g. a future code
	// path that explicitly closes the popup), Fyne does not
	// blow up trying to run a default cleanup that's not
	// defined. Hide() is the canonical "close" path now.
	p.win.SetCloseIntercept(p.Hide)
	p.win.SetOnClosed(func() {})
	p.win.Canvas().SetOnTypedKey(p.handleKey)
	// Canvas-level typed-rune handler. Fires only when no
	// widget has focus (list drops chars silently, so this
	// is the only way to capture printable input while the
	// popup is in plain list mode or slash-search mode).
	p.win.Canvas().SetOnTypedRune(p.handleTypedRune)

	// ── Search bar (slash-mode prompt) ────────────────────────────
	// The search bar is shown only while p.searchMode is true.
	// We hide it by default so an empty popup looks clean until
	// the user presses "/" to enter search.
	p.searchBar = canvas.NewText("", theme.Color(theme.ColorNameForeground))
	p.searchBar.Alignment = fyne.TextAlignCenter
	p.searchBar.TextSize = theme.TextSize()
	p.searchBar.TextStyle = fyne.TextStyle{Monospace: true}
	p.searchBar.Hide()

	// ── Search area (bar + divider) ─────────────────────────────────
	// The whole area is toggled as one unit so showing it triggers a
	// reliable re-layout (a bare label Show/Hide can leave the popup
	// visually unchanged until another refresh occurs).
	//
	// The divider is two stacked bands: a 2px solid line plus a 3px
	// soft shadow below it, so the boundary between the search bar and
	// the clip list reads clearly.
	line := canvas.NewRectangle(theme.Color(theme.ColorNameSeparator))
	line.SetMinSize(fyne.NewSize(0, 2))
	shadow := canvas.NewRectangle(theme.Color(theme.ColorNameShadow))
	shadow.SetMinSize(fyne.NewSize(0, 3))
	// The bar text is centred in a fixed-height strip via
	// searchBarLayout so its vertical position is pixel-exact.
	p.searchArea = container.NewVBox(
		container.New(&searchBarLayout{height: searchBarHeight, nudgeY: searchBarTextNudgeY}, p.searchBar),
		container.NewVBox(line, shadow),
	)
	p.searchArea.Hide()

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

	// Footer is a 2×2 grid of shortcut hints, one per corner:
	//   search:/   del:supr
	//   exit:esc   pin:p
	// Using the caption text size keeps the strip compact so the four
	// hints fit on the fixed 400px popup height without crowding.
	hintSearch := widget.NewLabelWithStyle("search:/", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	hintSearch.SizeName = theme.SizeNameCaptionText
	hintDel := widget.NewLabelWithStyle("del:supr", fyne.TextAlignTrailing, fyne.TextStyle{Monospace: true})
	hintDel.SizeName = theme.SizeNameCaptionText
	hintExit := widget.NewLabelWithStyle("exit:esc", fyne.TextAlignLeading, fyne.TextStyle{Monospace: true})
	hintExit.SizeName = theme.SizeNameCaptionText
	hintPin := widget.NewLabelWithStyle("pin:p", fyne.TextAlignTrailing, fyne.TextStyle{Monospace: true})
	hintPin.SizeName = theme.SizeNameCaptionText

	footer := container.NewGridWithColumns(2,
		hintSearch, hintDel,
		hintExit, hintPin,
	)

	// ── Layout ────────────────────────────────────────────────────────
	content := container.NewBorder(
		p.searchArea,
		container.NewPadded(footer),
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

// tightVBox is a layout that stacks items vertically with a fixed gap.
// The gap may be negative to pull items closer and cancel the internal
// padding widget.Label adds below its text (which otherwise leaves a
// visible blank band between the preview and the timestamp).
type tightVBox struct {
	gap float32
}

func (t *tightVBox) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		h := obj.MinSize().Height
		obj.Resize(fyne.NewSize(size.Width, h))
		obj.Move(fyne.NewPos(0, y))
		y += h + t.gap
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
	if len(objects) > 1 && t.gap < 0 {
		// Only subtract gap between visible items, not after the last.
		visible := 0
		for _, obj := range objects {
			if obj.Visible() {
				visible++
			}
		}
		h += t.gap * float32(visible-1)
	}
	return fyne.NewSize(w, h)
}

// ─────────────────────────────────────────────────────────────────────────────
// Search bar layout — pixel-exact horizontal + vertical centering
// ─────────────────────────────────────────────────────────────────────────────

// searchBarLayout centers its single child (the search bar text) both
// horizontally and vertically inside a strip of fixed searchBarHeight.
// It exists because canvas.Text's line box is taller than the visible
// glyphs: a bare VBox/Padded container left-aligns the text and lets
// the line-box metrics push the glyphs off-center vertically. This
// layout re-centers the object box, then searchBarTextNudgeY fine-tunes
// the optical centre by a pixel or two.
type searchBarLayout struct {
	height float32
	nudgeY float32
}

func (l *searchBarLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		min := obj.MinSize()
		x := (size.Width - min.Width) / 2
		y := (size.Height-min.Height)/2 + l.nudgeY
		obj.Move(fyne.NewPos(x, y))
		obj.Resize(fyne.NewSize(min.Width, min.Height))
	}
}

func (l *searchBarLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	w := float32(0)
	for _, obj := range objects {
		if !obj.Visible() {
			continue
		}
		if m := obj.MinSize().Width; m > w {
			w = m
		}
	}
	return fyne.NewSize(w, l.height)
}

// ─────────────────────────────────────────────────────────────────────────────
// List row construction — Ultra-compact with zero-gap layout
// ─────────────────────────────────────────────────────────────────────────────

type clipRow struct {
	container    *fyne.Container
	bgRect       *canvas.Rectangle
	previewLabel *widget.Label
	timeLabel    *canvas.Text
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

	// Detail label - small, subtle (fixed 10px to keep the row tight).
	// Using canvas.Text (instead of widget.Label) lets us set an
	// explicit TextSize; widget.Label always inherits the theme's
	// default text size which is too tall and leaves a visible gap
	// between the preview and the timestamp.
	timeLabel := canvas.NewText("", theme.Color(theme.ColorNameDisabled))
	timeLabel.TextSize = 10
	timeLabel.TextStyle = fyne.TextStyle{Italic: true}

	// Pin button
	pinBtn := widget.NewButtonWithIcon("", pinIcon(false), nil)
	pinBtn.Importance = widget.LowImportance

	// Delete button
	deleteBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), nil)
	deleteBtn.Importance = widget.LowImportance

	// Right side: icons horizontal
	rightSide := container.NewHBox(pinBtn, deleteBtn)

	// Main content: preview + time. A small negative gap pulls the
	// timestamp up to cancel the landing-padding that widget.Label
	// adds below the preview text, so the two lines sit close together
	// instead of with a blank band between them.
	leftContent := container.New(&tightVBox{gap: -4}, previewLabel, timeLabel)

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
		row.previewLabel.SetText(normalizePreviewText(clip.Content, previewMaxRunes))
	}

	// Show the relative timestamp in the lower row so the popup
	// still communicates when the clip was copied. The main preview
	// above is already constrained to a single line via truncation.
	if clip.Type == domain.ClipTypeImage {
		row.timeLabel.Text = relativeTime(clip.CreatedAt)
	} else {
		row.timeLabel.Text = relativeTime(clip.CreatedAt)
	}
	row.timeLabel.Refresh()

	// Update pin icon appearance
	row.pinBtn.SetIcon(pinIcon(clip.IsFavorite))

	// Update button handlers
	clipID := clip.ID
	clipIdx := id
	row.clipID = clipID
	row.index = int(clipIdx)

	row.pinBtn.OnTapped = func() {
		p.toggleFavorite(clipID, int(clipIdx))
		p.win.Canvas().Focus(nil)
	}
	row.deleteBtn.OnTapped = func() {
		p.confirmDelete(clipID, int(clipIdx))
		p.win.Canvas().Focus(nil)
	}

	// Background: a translucent green tint when pinned, transparent
	// when not. We do not reuse theme.ColorNamePrimary here because
	// that is now a solid green (for buttons/checkmarks); a solid row
	// background would wash out the preview text. The 64-alpha green
	// keeps the pinned row softly highlighted instead.
	if clip.IsFavorite {
		row.bgRect.FillColor = color.RGBA{R: 46, G: 125, B: 50, A: 64}
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
	p.selectedID = id
	// Return focus to canvas after manual selection so navigation keys continue to work
	p.win.Canvas().Focus(nil)
	// Suppress auto-paste when the selection came from
	// handleArrow (programmatic) — the navigation has explicitly
	// stepped to a row and the user must press Enter to commit.
	if p.navigating {
		return
	}
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
		// Escape while searching: exit search and stay.
		// Escape in plain mode: hide the popup.
		if p.searchMode {
			p.exitSearchMode(false)
			return
		}
		p.Hide()

	case fyne.KeyReturn, fyne.KeyEnter:
		p.handleEnter()

	case fyne.KeyUp:
		p.handleArrow(-1)

	case fyne.KeyDown:
		p.handleArrow(+1)

	case fyne.KeyBackspace, fyne.KeyDelete:
		if p.searchMode {
			// Backspace/Delete inside the search query: pop one
			// rune and re-apply the filter.
			if len(p.query) > 0 {
				p.popQueryRune()
				p.applyQuery()
			}
			return
		}
		// Supr/Delete outside search mode deletes the highlighted clip.
		if ev.Name == fyne.KeyDelete {
			p.handleDeleteHighlighted()
		}

	case fyne.KeySlash:
		// "/" begins slash-mode search — same convention as
		// bin/nnn/vifm/etc. Only when NOT in search mode;
		// subsequent "/" characters are part of the query.
		if !p.searchMode {
			// Arm the rune suppressor so the matching
			// TypedRune for this same physical keypress is
			// dropped instead of polluting the query with
			// a literal slash.
			p.suppressSlashRune = true
			p.enterSearchMode()
		}

	case fyne.KeyJ:
		// j moves down (vim mode). Skip when typing in search
		// mode is unnecessary because j/k don't have a literal
		// role there — we just gate on searchMode so they
		// remain letters in the query.
		if p.readVimNavigationSetting() && !p.searchMode {
			p.handleArrow(+1)
		}

	case fyne.KeyK:
		if p.readVimNavigationSetting() && !p.searchMode {
			p.handleArrow(-1)
		}

	case fyne.KeyP:
		// "p" toggles the pinned state of the highlighted clip.
		// Only in plain list mode; in search mode "p" is a literal
		// query character handled by handleTypedRune.
		if !p.searchMode {
			p.handlePinHighlighted()
		}
	}
}

// handleTypedRune is the canvas-level rune (printable character)
// handler. Fyne only fires it when no widget has focus, which
// is exactly our state on popup open (no widget claims focus by
// default — the list does not take focus on row click).
//
// Slash-mode semantics:
//   - In plain mode: rune is dropped (no text input is
//     meaningful outside the search affordance).
//   - In search mode: rune is appended to p.query and the
//     filter is re-applied.
func (p *PopupWindow) handleTypedRune(r rune) {
	if !p.searchMode {
		// Don't track character input outside of search mode.
		// It avoids surprising interactions with arrow keys
		// (the user expects a focused list, not a text field).
		return
	}
	// Skip keyboard shortcuts like Ctrl-something; only
	// printable characters accumulated.
	if r < 0x20 || r == 0x7f {
		return
	}
	// The first '/' that initiated search mode is also
	// delivered as a TypedRune — drop it so the query stays
	// the user's literal intent (e.g. "abc") rather
	// than "/abc".
	if p.suppressSlashRune {
		p.suppressSlashRune = false
		if r == '/' {
			// Update the bar only — query itself remains
			// empty so the filter doesn't reject clips that
			// contain only the user's typed text.
			updateSearchBar(p)
			return
		}
	}
	p.query += string(r)
	p.applyQuery()
}

// handleEnter triggers paste for the currently selected row.
// If no row is selected, picks index 0 (top of the list).
func (p *PopupWindow) handleEnter() {
	if len(p.filtered) == 0 {
		return
	}
	id := p.selectedID
	if id < 0 || id >= len(p.filtered) {
		id = 0
	}
	// Make sure the highlighted row matches what we paste —
	// even if no arrow key was pressed, calling Select() also
	// ensures any UI state (scroll position, last-clicked
	// highlight) is consistent before paste. The OnSelected
	// callback is suppressed via the navigating flag.
	p.navigating = true
	p.clipList.Select(id)
	p.navigating = false
	p.pasteClip(p.filtered[id])
}

// highlightedClip returns the clip under the current selection, or
// (false) when the list is empty or the selection is out of range.
func (p *PopupWindow) highlightedClip() (domain.Clip, int, bool) {
	if len(p.filtered) == 0 || p.selectedID < 0 {
		return domain.Clip{}, 0, false
	}
	id := p.selectedID
	if int(id) >= len(p.filtered) {
		return domain.Clip{}, 0, false
	}
	return p.filtered[id], int(id), true
}

// handleDeleteHighlighted deletes the currently highlighted clip.
// Shows the same confirmation dialog as the row's delete button.
// No-op when there is no selection.
func (p *PopupWindow) handleDeleteHighlighted() {
	clip, idx, ok := p.highlightedClip()
	if !ok {
		return
	}
	p.confirmDelete(clip.ID, idx)
}

// handlePinHighlighted toggles the pinned state of the currently
// highlighted clip. No-op when there is no selection.
func (p *PopupWindow) handlePinHighlighted() {
	clip, idx, ok := p.highlightedClip()
	if !ok {
		return
	}
	p.toggleFavorite(clip.ID, idx)
}

// handleArrow moves the highlighted row by delta (signed).
//   - delta == +1 : move down by 1
//   - delta == -1 : move up by 1
//
// Clamps at 0 and len(filtered)-1. Scrolls the selection into
// view. No-op when the list is empty.
func (p *PopupWindow) handleArrow(delta int) {
	if len(p.filtered) == 0 {
		return
	}
	id := p.selectedID
	if id < 0 {
		id = 0
	}
	id += delta
	if id < 0 {
		id = 0
	}
	if id >= len(p.filtered) {
		id = len(p.filtered) - 1
	}
	// Programmatic Select fires onClipSelected synchronously.
	// onClipSelected checks the navigating flag and only
	// updates selectedID — it does not invoke pasteClip.
	p.navigating = true
	p.clipList.Select(id)
	p.navigating = false
}

// enterSearchMode switches the popup into slash-search mode.
// Called when the user types "/" in plain mode (see handleKey).
//
// We update the search bar immediately so the user sees the
// "/" prompt without delay. We deliberately do NOT call
// applyQuery here — the first TypedRune after enabling search
// mode will call applyQuery synchronously. Calling it again
// from enterSearchMode would race with the in-flight
// loadClips goroutine: loadClips sets allClips/filtered and
// enterSearchMode could re-truncate filtered before that
// completes.
func (p *PopupWindow) enterSearchMode() {
	p.searchMode = true
	updateSearchBar(p)
}

// exitSearchMode leaves slash-search mode.
//
// resetQuery=true  → drop the query, restore the unfiltered list.
// resetQuery=false → keep the query (e.g. when the popup is
//
//	closed and reopened, we keep the query so
//	the search persists across hide/show).
//
// hidePopup=true   → also hide the popup after exiting
//
//	(used by Esc-in-search when the user is
//	cancelling). This is currently unused
//	because Esc-in-search only exits, not
//	hides, but the parameter exists for
//	forward-compatibility.
func (p *PopupWindow) exitSearchMode(resetQuery bool) {
	p.searchMode = false
	if resetQuery {
		p.query = ""
	}
	updateSearchBar(p)
	if resetQuery || p.query == "" {
		// Allocate a fresh copy to avoid aliasing with
		// allClips when filtered has been mutated by
		// applyQuery.
		fresh := make([]domain.Clip, len(p.allClips))
		copy(fresh, p.allClips)
		p.filtered = fresh
		p.selectedID = -1
		p.refreshList()
		p.updateCountLabel()
	} else {
		p.applyQuery()
	}
}

// applyQuery filters allClips by case-insensitive substring
// match against p.query and updates p.filtered + list refresh.
//
// We deliberately use simple substring matching on Content +
// the SVG/image paths can't be searched, but the FTS5-backed
// history repo also has a Search() method — leaving this as
// a simple implementation keeps the popup responsive on very
// large clip histories; the FTS5 path is reserved for the
// dedicated "search" tray-menu wire-up if needed later.
func (p *PopupWindow) applyQuery() {
	q := p.query
	// Allocate a fresh slice for filtered rather than reusing
	// the backing array of allClips. Reusing would be unsafe
	// because loadClips sets allClips = clips and filtered =
	// clips on initial load — they share the same underlying
	// array. append-ing into the truncated filtered slice would
	// then mutate the allClips data, leading to silent
	// corruption as soon as loadClips runs (or we exit search
	// mode and try to "restore" from a no-longer-pristine
	// backing array).
	fresh := make([]domain.Clip, 0, len(p.allClips))
	if q == "" {
		fresh = append(fresh, p.allClips...)
	} else {
		lq := strings.ToLower(q)
		for _, c := range p.allClips {
			if strings.Contains(strings.ToLower(c.Content), lq) {
				fresh = append(fresh, c)
			}
		}
	}
	p.filtered = fresh
	p.selectedID = -1
	p.refreshList()
	p.updateCountLabel()
	// Keep the search bar in sync with the active query so the
	// user sees what they typed. handleTypedRune appends the
	// rune, then calls applyQuery, so this is the canonical
	// moment to refresh the visual.
	updateSearchBar(p)
}

// popQueryRune removes the last rune from p.query. UTF-8 aware
// because user input may include multi-byte characters.
func (p *PopupWindow) popQueryRune() {
	if p.query == "" {
		return
	}
	runes := []rune(p.query)
	if len(runes) == 0 {
		return
	}
	p.query = string(runes[:len(runes)-1])
}

// updateSearchBar sets the search bar content and visibility.
// In search mode it shows "<query>" (or a dimmed placeholder when the
// query is empty); otherwise it hides the whole search area.
func updateSearchBar(p *PopupWindow) {
	if p.searchBar == nil || p.searchArea == nil {
		return
	}
	if p.searchMode {
		if p.query == "" {
			p.searchBar.Text = "type to search..."
			p.searchBar.Color = theme.Color(theme.ColorNameDisabled)
			p.searchBar.TextStyle = fyne.TextStyle{Italic: true}
		} else {
			p.searchBar.Text = p.query
			p.searchBar.Color = theme.Color(theme.ColorNameForeground)
			p.searchBar.TextStyle = fyne.TextStyle{Monospace: true}
		}
		p.searchBar.Show()
		p.searchArea.Show()
	} else {
		p.searchBar.Text = ""
		p.searchBar.Hide()
		p.searchArea.Hide()
	}
	p.searchBar.Refresh()
	// Force a re-layout at the window level, not just on the search
	// area. Fyne does not reliably re-layout the popup when only a
	// nested child toggles Show/Hide; refreshing the window content
	// guarantees the bar (and its divider) appear as soon as "/" is
	// pressed, not only after the first character is typed.
	p.searchArea.Refresh()
	if p.win != nil && p.win.Content() != nil {
		p.win.Content().Refresh()
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UI state helpers
// ─────────────────────────────────────────────────────────────────────────────

func (p *PopupWindow) refreshList() {
	p.selectedID = -1
	p.clipList.UnselectAll()
	p.clipList.Refresh()
	if len(p.filtered) > 0 {
		p.clipList.ScrollToTop()
		// Highlight the first clip by default so the user always knows
		// which clip Enter will paste, and so the first arrow/j-k press
		// starts from row 0 (instead of skipping it). The navigating
		// flag suppresses the auto-paste that onClipSelected would
		// otherwise trigger for a programmatic Select.
		p.navigating = true
		p.clipList.Select(0)
		p.navigating = false
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

// relativeTime formats the clipboard capture time into a compact
// human-friendly age string for display in the popup rows.
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

// normalizePreviewText collapses whitespace so pasted text with newlines or
// repeated spaces stays on one visual line in the popup row, then truncates it
// to the same compact preview width used elsewhere.
func normalizePreviewText(content string, maxRunes int) string {
	if maxRunes <= 0 || content == "" {
		return ""
	}
	text := strings.Join(strings.Fields(content), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
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
