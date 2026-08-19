// Keyboard-navigable confirmation dialog.
//
// Fyne's stock dialog.ShowConfirm only activates a focused button with
// Space — it ignores Enter and Escape, and its Tab navigation relies on
// the global focus chain. This dialog replaces it for destructive
// actions so the keyboard can drive it fully:
//
//	Tab / arrows   move the selection between Cancel and Delete
//	Enter / Space  activate the selected button
//	Escape         cancel (no action)
//
// The dialog is itself a fyne.Focusable. While it is open it owns the
// canvas focus, so every key event routes to TypedKey below instead of
// to the popup window's list-navigation handler.
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// confirmDialog is a modal Cancel/Delete confirmation with full keyboard
// navigation. It replaces dialog.ShowConfirm for destructive actions.
type confirmDialog struct {
	widget.BaseWidget

	parent *PopupWindow
	clipID int64
	idx    int

	popup     *widget.PopUp
	cancelBtn *widget.Button
	deleteBtn *widget.Button
	content   fyne.CanvasObject

	// selected is the currently highlighted button: 0 = cancel, 1 = delete.
	selected int
}

// newConfirmDialog builds (but does not show) a confirmation dialog that
// deletes clipID from the popup's list when confirmed.
func newConfirmDialog(parent *PopupWindow, clipID int64, idx int) *confirmDialog {
	d := &confirmDialog{parent: parent, clipID: clipID, idx: idx}
	d.ExtendBaseWidget(d)

	d.cancelBtn = widget.NewButton("Cancel", func() { d.hide(false) })
	d.deleteBtn = widget.NewButton("Delete", func() { d.hide(true) })

	msg := widget.NewLabel("Remove this item from clipboard history? This cannot be undone.")
	msg.Wrapping = fyne.TextWrapWord

	btnRow := container.NewHBox(d.cancelBtn, d.deleteBtn)
	d.content = container.NewVBox(msg, container.NewPadded(btnRow))

	return d
}

// CreateRenderer implements fyne.Widget.
func (d *confirmDialog) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(d.content)
}

// show displays the dialog as a modal overlay and grabs canvas focus so
// every subsequent key routes to this dialog's TypedKey. The delete
// button is selected by default (the action the user is confirming).
func (d *confirmDialog) show() {
	d.popup = widget.NewModalPopUp(d, d.parent.win.Canvas())
	d.popup.Show()
	d.setSelected(1) // default to Delete
	d.parent.win.Canvas().Focus(d)
}

// hide closes the dialog and restores focus to the popup so its list
// navigation keys work again. When confirmed, the clip is deleted.
func (d *confirmDialog) hide(confirmed bool) {
	if d.popup != nil {
		d.popup.Hide()
		d.popup = nil
	}
	d.parent.win.Canvas().Focus(nil)
	if confirmed {
		d.parent.deleteClip(d.clipID, d.idx)
	}
}

// setSelected highlights button i (0 = Cancel, 1 = Delete) by toggling
// its Importance, and refreshes both buttons so the highlight is drawn.
func (d *confirmDialog) setSelected(i int) {
	d.selected = i
	switch i {
	case 0:
		d.cancelBtn.Importance = widget.HighImportance
		d.deleteBtn.Importance = widget.LowImportance
	default:
		d.cancelBtn.Importance = widget.LowImportance
		d.deleteBtn.Importance = widget.HighImportance
	}
	d.cancelBtn.Refresh()
	d.deleteBtn.Refresh()
}

// activate triggers the currently selected button's action.
func (d *confirmDialog) activate() {
	if d.selected == 0 {
		d.hide(false)
	} else {
		d.hide(true)
	}
}

// ─── fyne.Focusable ─────────────────────────────────────────────────────────

// FocusGained is a no-op; selection state is managed in setSelected.
func (d *confirmDialog) FocusGained() {}

// FocusLost is a no-op.
func (d *confirmDialog) FocusLost() {}

// TypedRune ignores printable characters — the dialog has no text input.
func (d *confirmDialog) TypedRune(rune) {}

// TypedKey drives the dialog from the keyboard:
//
//	Tab / arrows   move the selection between Cancel and Delete
//	Enter / Space  activate the selected button
//	Escape         cancel
func (d *confirmDialog) TypedKey(ev *fyne.KeyEvent) {
	switch ev.Name {
	case fyne.KeyEscape:
		d.hide(false)
	case fyne.KeyTab, fyne.KeyRight, fyne.KeyDown:
		d.setSelected(1 - d.selected)
	case fyne.KeyLeft, fyne.KeyUp:
		d.setSelected(1 - d.selected)
	case fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace:
		d.activate()
	}
}

// ─── fyne.Tabbable ──────────────────────────────────────────────────────────

// AcceptsTab makes Tab reach TypedKey (instead of Fyne's global focus
// chain), so it moves the dialog's selection rather than tabbing into
// the popup list underneath.
func (d *confirmDialog) AcceptsTab() bool { return true }
