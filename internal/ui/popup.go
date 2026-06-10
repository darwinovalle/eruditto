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
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/darwinovalle/eruditto/internal/clipboard"
	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/history"
)

// popupPageSize is the number of clips loaded per query.
// 200 covers 10+ screens of content at typical row heights.
// The SQLite query for 200 rows from an indexed table takes < 5ms.
const popupPageSize = 200

// previewMaxRunes is the character limit for the text preview in each row.
const previewMaxRunes = 10

// PopupWindow is the clipboard history picker.
//
// Lifecycle:
//
//	New → (hidden) → Show() ↔ Hide()
//	                  ↓
//	           loads clips async
//	           filters on search input
//	           calls RestoreClip on selection
//
// Thread safety: all Fyne widget interactions must happen on the UI
// goroutine. Async operations use fyne.CurrentApp().Driver().CanvasForObject
// or the simpler pattern of sending results back via a channel and
// calling widget methods inside a go func that the Fyne scheduler runs.
// We use the binding package so data updates are automatically safe.
type PopupWindow struct {
	app      fyne.App
	win      fyne.Window
	clipSvc  *clipboard.Service
	repo     *history.Repository

	allClips []domain.Clip
	filtered []domain.Clip

	searchEntry *widget.Entry
	clipList    *widget.List
	countLabel  *widget.Label
	statusLabel *widget.Label

	// clipChanged is signalled by NotifyClipChanged() whenever the
	// history changes (new clip, delete, favorite toggle).
	// The background listener reloads the list if the popup is visible.
	clipChanged chan struct{}

	built bool
}

// NewPopupWindow constructs the popup. The window is hidden until Show().
//
//   - app     : the running Fyne application (needed to create windows)
//   - clipSvc : clipboard service — used for RestoreClip
//   - repo    : history repository — used to load clips and toggle favorites
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
		clipChanged: make(chan struct{}, 1), // buffered: never blocks the sender
	}
}

// NotifyClipChanged signals the popup that clipboard history has changed.
// Safe to call from any goroutine. Non-blocking: if a signal is already
// pending, the new one is dropped (the pending reload covers it).
func (p *PopupWindow) NotifyClipChanged() {
	select {
	case p.clipChanged <- struct{}{}:
	default: // already pending, drop
	}
}

// StartListening starts the background goroutine that watches for
// clip changes and reloads the popup list when it is visible.
// Call once from main.go after construction. ctx is the root context.
func (p *PopupWindow) StartListening(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.clipChanged:
				// Only reload if the window is built and visible.
				fyne.Do(func() {
					if p.built && p.win.Content() != nil {
						go p.loadClips(p.currentQuery())
					}
				})
			}
		}
	}()
}

// currentQuery returns the current search entry text, or "" if not built.
// Must be called on the UI goroutine.
func (p *PopupWindow) currentQuery() string {
	if p.searchEntry == nil {
		return ""
	}
	return p.searchEntry.Text
}

// Show opens the popup and reloads clips fresh from the DB.
// Safe to call from any goroutine.
func (p *PopupWindow) Show() {
	fyne.Do(func() {
		if !p.built {
			p.build()
		}
		p.searchEntry.SetText("")
		p.setStatus("Loading...")
		p.win.Show()
		p.win.RequestFocus()
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

// build constructs all widgets and the window layout.
// Called exactly once; subsequent Show() calls reuse the built window.
func (p *PopupWindow) build() {
	p.win = p.app.NewWindow("Eruditto — Clipboard History")
	p.win.Resize(fyne.NewSize(100, 300))
	p.win.CenterOnScreen()

	// Close on focus loss — standard Ditto-style behavior.
	// When the user clicks elsewhere the window hides.
	p.win.SetOnClosed(func() {}) // prevent destroy; we reuse the window
	p.win.Canvas().SetOnTypedKey(p.handleKey)

	// ── Search entry ──────────────────────────────────────────────────
	p.searchEntry = widget.NewEntry()
	p.searchEntry.SetPlaceHolder("Search clipboard history…")
	p.searchEntry.OnChanged = p.onSearchChanged

	// ── Clip list ─────────────────────────────────────────────────────
	p.clipList = widget.NewList(
		// Length: driven by len(filtered)
		func() int { return len(p.filtered) },
		// Create row template
		p.createRow,
		// Update row with data
		p.updateRow,
	)
	p.clipList.OnSelected = p.onClipSelected

	// ── Footer ────────────────────────────────────────────────────────
	p.countLabel = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{})
	p.statusLabel = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Italic: true})

	hint := widget.NewLabelWithStyle(
		"↵ paste · Esc close · ★ pin",
		fyne.TextAlignTrailing,
		fyne.TextStyle{Monospace: true},
	)

	footer := container.NewHBox(p.countLabel, layout.NewSpacer(), hint)

	// ── Layout ────────────────────────────────────────────────────────
	// Search at top, list in middle (takes all remaining space), footer fixed at bottom.
	content := container.NewBorder(
		container.NewVBox(
			p.searchEntry,
			p.statusLabel,
			widget.NewSeparator(),
		),
		container.NewVBox(
			widget.NewSeparator(),
			footer,
		),
		nil, nil,
		p.clipList,
	)

	p.win.SetContent(content)
	p.built = true
}

// ─────────────────────────────────────────────────────────────────────────────
// List row construction
// ─────────────────────────────────────────────────────────────────────────────

// clipRow is the struct behind each list row's template widget.
// We store sub-widgets as fields so updateRow can reach them.
type clipRow struct {
	container    *fyne.Container
	previewLabel *widget.Label
	timeLabel    *widget.Label
	starBtn      *widget.Button
	deleteBtn    *widget.Button
	index        int // current data index; set by updateRow
}

// rowCache maps each container returned by createRow back to its clipRow,
// so updateRow can access widgets directly without Objects[] index guessing.
// Entries live for the lifetime of the list widget.
var rowCache = map[fyne.CanvasObject]*clipRow{}

// createRow returns a new template container for a list row.
// Fyne calls this to create the reusable cell widgets.
func (p *PopupWindow) createRow() fyne.CanvasObject {
	row := &clipRow{
		previewLabel: widget.NewLabel(""),
		timeLabel: widget.NewLabelWithStyle("", fyne.TextAlignTrailing,
			fyne.TextStyle{Italic: true}),
		starBtn:   widget.NewButtonWithIcon("", theme.RadioButtonIcon(), nil),
		deleteBtn: widget.NewButtonWithIcon("", theme.DeleteIcon(), nil),
	}

	row.previewLabel.Truncation = fyne.TextTruncateEllipsis
	row.previewLabel.Wrapping = fyne.TextWrapOff
	row.starBtn.Importance = widget.LowImportance
	row.deleteBtn.Importance = widget.DangerImportance

	row.container = container.NewBorder(
		nil, nil, nil,
		container.NewHBox(row.timeLabel, row.starBtn, row.deleteBtn),
		row.previewLabel,
	)

	// Register so updateRow can look up widgets by container pointer.
	rowCache[row.container] = row
	return row.container
}

// updateRow populates a reused row container with data from filtered[i].
// Fyne calls this whenever a row scrolls into view.
func (p *PopupWindow) updateRow(id widget.ListItemID, obj fyne.CanvasObject) {
	if int(id) >= len(p.filtered) {
		return
	}
	clip := p.filtered[id]

	row, ok := rowCache[obj]
	if !ok {
		return
	}

	if clip.Type == domain.ClipTypeImage {
		row.previewLabel.SetText("[image]")
	} else {
		row.previewLabel.SetText(clip.DisplayContent(previewMaxRunes))
	}
	row.timeLabel.SetText(relativeTime(clip.CreatedAt))
	if clip.IsFavorite {
		row.starBtn.SetIcon(theme.RadioButtonCheckedIcon())
	} else {
		row.starBtn.SetIcon(theme.RadioButtonIcon())
	}

	clipID := clip.ID
	clipIdx := id
	row.starBtn.OnTapped = func() { p.toggleFavorite(clipID, clipIdx) }
	row.deleteBtn.OnTapped = func() { p.confirmDelete(clipID, clipIdx) }
}

// ─────────────────────────────────────────────────────────────────────────────
// Data loading
// ─────────────────────────────────────────────────────────────────────────────

// loadClips fetches clips from the repository in a background goroutine
// and updates the list on the UI thread via a channel.
func (p *PopupWindow) loadClips(query string) {
    ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
    defer cancel()

    // query := p.searchEntry.Text

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
// Search
// ─────────────────────────────────────────────────────────────────────────────

// onSearchChanged is called on every keystroke in the search entry.
// For non-empty queries it fires a fresh database search (FTS5 is fast).
// For empty queries it restores the full allClips slice without a DB round-trip.
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

// onClipSelected is called when the user single-clicks a row.
// Double-click in Fyne fires OnSelected twice in quick succession;
// we treat a single selection as the paste trigger (matches Ditto UX).
func (p *PopupWindow) onClipSelected(id widget.ListItemID) {
	if id >= len(p.filtered) {
		return
	}
	clip := p.filtered[id]
	p.pasteClip(clip)
}

// pasteClip calls RestoreClip and hides the popup.
func (p *PopupWindow) pasteClip(clip domain.Clip) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := p.clipSvc.RestoreClip(ctx, clip); err != nil {
		dialog.ShowError(err, p.win)
		return
	}
	p.Hide()
}

// toggleFavorite flips the favorite flag for a clip and refreshes the row.
func (p *PopupWindow) toggleFavorite(clipID int64, idx int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	newVal, err := p.repo.ToggleFavorite(ctx, clipID)
	if err != nil {
		dialog.ShowError(err, p.win)
		return
	}

	// Update in-memory slice so the row refreshes without a reload.
	if idx < len(p.filtered) {
		p.filtered[idx].IsFavorite = newVal
	}
	// Also update allClips so a search-clear doesn't revert the icon.
	for i := range p.allClips {
		if p.allClips[i].ID == clipID {
			p.allClips[i].IsFavorite = newVal
			break
		}
	}

	p.clipList.RefreshItem(idx)
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

// handleKey processes global key events for the popup window.
//
//	Esc    → hide popup
//	Enter  → paste selected clip
//	↑ / ↓  → move list selection
func (p *PopupWindow) handleKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyEscape:
		p.Hide()

	case fyne.KeyReturn, fyne.KeyEnter:
		if len(p.filtered) == 0 {
			return
		}
		// Use the currently selected item, defaulting to index 0.
		idx := 0
		// Fyne's List doesn't expose the selected index directly in all
		// versions; we use OnSelected wiring and track it separately if needed.
		// For now, Enter on the search entry pastes the top result.
		p.pasteClip(p.filtered[idx])

	case fyne.KeyUp:
		// Move list selection up — delegate to the list widget.
		p.clipList.ScrollToTop()

	case fyne.KeyDown:
		// Shift focus to list for arrow navigation.
		p.win.Canvas().Focus(p.clipList)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UI state helpers
// ─────────────────────────────────────────────────────────────────────────────

// refreshList updates the list widget after the underlying slice changes.
func (p *PopupWindow) refreshList() {
    p.clipList.UnselectAll()
    p.clipList.Refresh()
    if len(p.filtered) > 0 {
        p.clipList.ScrollToTop()
    }
}

// setStatus shows or hides the status label (loading / empty state).
func (p *PopupWindow) setStatus(msg string) {
	p.statusLabel.SetText(msg)
	if msg == "" {
		p.statusLabel.Hide()
	} else {
		p.statusLabel.Show()
	}
}

// updateCountLabel refreshes the footer count string.
//
//	"1,247 clips · 3 favorites"
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
	case favs == 0:
		p.countLabel.SetText(fmt.Sprintf("%s clips", formatInt(total)))
	default:
		p.countLabel.SetText(fmt.Sprintf("%s clips · %s pinned",
			formatInt(total), formatInt(favs)))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Pure helpers (no Fyne dependency)
// ─────────────────────────────────────────────────────────────────────────────

// relativeTime converts an absolute time to a human-friendly relative string.
//
//	< 1 minute ago  → "just now"
//	< 1 hour ago    → "5 minutes ago"
//	< 24 hours ago  → "3 hours ago"
//	< 7 days ago    → "2 days ago"
//	otherwise       → "Jan 2"
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

// formatInt adds thousands separators to an integer.
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

// removeByID returns a new slice with the clip matching id removed.
func removeByID(clips []domain.Clip, id int64) []domain.Clip {
	out := make([]domain.Clip, 0, len(clips))
	for _, c := range clips {
		if c.ID != id {
			out = append(out, c)
		}
	}
	return out
}

// Ensure unused imports are referenced (canvas is used for separators
// in potential future extensions; kept for the build).
