// Eruditto — a free, lightweight clipboard manager for Linux.
//
// This file is the composition root: the only place in the codebase
// that knows about all packages and wires them together.
//
// Startup sequence:
//  1. Parse flags / enforce single instance
//  2. Initialize logger
//  3. Ensure XDG directories exist
//  4. Open database and run migrations
//  5. Initialize services (settings, images, history, clipboard)
//  6. Initialize hotkey manager
//  7. Initialize Fyne app + UI windows
//  8. Initialize system tray
//  9. Start clipboard monitor goroutine
// 10. Register global hotkey
// 11. Start Fyne event loop (blocks until quit)
// 12. Ordered shutdown
//
// Shutdown order (enforced):
//  1. Cancel root context → signals all goroutines to stop
//  2. Wait for clipboard service to drain (blocks on done channel)
//  3. Close hotkey manager
//  4. Close database (after all writers have stopped)
//
// Closing the database before the monitor stops would cause the
// monitor's final repo.Insert call to fail with a confusing error.
// The order above prevents that.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"fyne.io/fyne/v2/app"

	"github.com/darwinovalle/eruditto/internal/clipboard"
	"github.com/darwinovalle/eruditto/internal/database"
	"github.com/darwinovalle/eruditto/internal/domain"
	"github.com/darwinovalle/eruditto/internal/history"
	"github.com/darwinovalle/eruditto/internal/hotkeys"
	"github.com/darwinovalle/eruditto/internal/images"
	"github.com/darwinovalle/eruditto/internal/settings"
	"github.com/darwinovalle/eruditto/internal/tray"
	"github.com/darwinovalle/eruditto/internal/ui"
	"github.com/darwinovalle/eruditto/pkg/xdg"
)

const (
	version     = "dev"
	appID       = "io.github.darwinovalle.eruditto"
	lockFile    = "eruditto.lock"   // under XDG data dir
	socketFile  = "eruditto.sock"   // under XDG data dir — IPC for --popup
)

func main() {
	startTime := time.Now()

	// ── 1. Flags ──────────────────────────────────────────────────────
	showPopup := flag.Bool("popup", false, "Show the clipboard history popup (forwards to running instance)")
	debugLog := flag.Bool("debug", false, "Enable debug-level logging")
	flag.Parse()

	// ── 2. Logger ─────────────────────────────────────────────────────
	logLevel := slog.LevelInfo
	if *debugLog {
		logLevel = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(log)

	log.Info("eruditto starting", "version", version)

	// ── 3. XDG directories ────────────────────────────────────────────
	if err := xdg.EnsureAll(); err != nil {
		log.Error("failed to create XDG directories", "error", err)
		os.Exit(1)
	}

	dataDir, err := xdg.DataDir()
	if err != nil {
		log.Error("failed to resolve XDG data directory", "error", err)
		os.Exit(1)
	}

	sockPath := filepath.Join(dataDir, socketFile)

	// ── 4. Single instance enforcement ────────────────────────────────
	//
	// Strategy: Unix domain socket.
	// If the socket already exists and is connectable, another instance
	// is running. We either forward --popup to it and exit, or just exit.
	//
	// If the socket exists but is not connectable (stale after a crash),
	// we remove it and claim it ourselves.
	if forwarded := tryForwardToExisting(sockPath, *showPopup, log); forwarded {
		os.Exit(0)
	}

	// If --popup was passed but there's no running instance, fall through
	// and start normally — first run with --popup is fine.

	// ── 5. Database ───────────────────────────────────────────────────
	dbPath := filepath.Join(dataDir, "eruditto.db")
	db, err := database.Open(dbPath, log)
	if err != nil {
		log.Error("failed to open database", "path", dbPath, "error", err)
		os.Exit(1)
	}

	// ── 6. Services ───────────────────────────────────────────────────
	settingsSvc := settings.New(db, log)
	imgStorage, err := images.New(log)
	if err != nil {
		log.Error("failed to initialize image storage", "error", err)
		os.Exit(1)
	}
	// imgStorage is consumed by the clipboard service below.
	// The service uses it to persist clipboard images to disk and
	// to read them back when restoring via the popup.

	historyRepo := history.New(db, log)
	clipReader := clipboard.NewAtottoReader()

	// ── 7. Root context ───────────────────────────────────────────────
	//
	// All goroutines receive this context. Cancelling it is step 1 of
	// the shutdown sequence. We cancel in the tray's OnQuit callback,
	// which runs when the user clicks "Quit Eruditto".
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	defer cancelRoot() // safety net — also cancelled explicitly in OnQuit

	// ── 8. Clipboard service ──────────────────────────────────────────
	clipSvc := clipboard.NewService(clipReader, historyRepo, imgStorage, settingsSvc, log)

	// ── 9. Hotkey manager ─────────────────────────────────────────────
	hotkeyMgr := hotkeys.New(log)

	// ── 10. Fyne application ──────────────────────────────────────────
	//
	// fyne.App must be created on the main goroutine before any window
	// is created. We create it here, before the tray starts, because
	// the tray's OnShowPopup callback references the popup window.
	fyneApp := app.NewWithID(appID)

	// Set up custom theme that responds to dark/light/system settings.
	// Read initial theme from database before applying.
	ctxTheme, cancelTheme := context.WithTimeout(rootCtx, 2*time.Second)
	themeSetting, _ := settingsSvc.Get(ctxTheme, domain.KeyTheme)
	cancelTheme()
	if themeSetting == "" {
		themeSetting = "dark"
	}
	ui.SetCurrentTheme(themeSetting)
	fyneApp.Settings().SetTheme(ui.NewErudittoTheme(ui.GetCurrentTheme))
	ui.SetThemeChangedCallback(func(mode string) {
		fyneApp.Settings().SetTheme(ui.NewErudittoTheme(ui.GetCurrentTheme))
	})

	// ── 11. UI windows ────────────────────────────────────────────────
	popupWin := ui.NewPopupWindow(fyneApp, clipSvc, historyRepo)

	// registerHotkey parses a shortcut string and registers it with the manager.
	// Centralises the string→Shortcut conversion so call sites stay clean.
	registerHotkey := func(shortcutStr string, handler func()) error {
		sc, err := hotkeys.ParseShortcut(shortcutStr)
		if err != nil {
			return fmt.Errorf("invalid shortcut %q: %w", shortcutStr, err)
		}
		return hotkeyMgr.Register(sc, handler)
	}

	unregisterHotkey := func(shortcutStr string) error {
		sc, err := hotkeys.ParseShortcut(shortcutStr)
		if err != nil {
			return nil // can't unregister what we can't parse
		}
		return hotkeyMgr.Unregister(sc)
	}

	// currentHotkey tracks the active shortcut string so we can
	// unregister it when the user changes it in settings.
	currentHotkey := domain.DefaultSettings[domain.KeyHotkey]

	settingsWin := ui.NewSettingsWindow(
		fyneApp,
		settingsSvc,
		hotkeyMgr,
		historyRepo,
		dataDir,
		func(newShortcut hotkeys.Shortcut) error {
			_ = unregisterHotkey(currentHotkey)

			err := registerHotkey(newShortcut.String(), func() {
				popupWin.Show()
			})

			if err == nil {
				currentHotkey = newShortcut.String()
			}

			return err
		},
	)

	// ── 12. System tray ───────────────────────────────────────────────
	t := tray.New(tray.Callbacks{
		OnShowPopup:    popupWin.Show,
		OnOpenSettings: settingsWin.Show,
		OnQuit: func() {
			log.Info("quit requested — beginning shutdown")

			// Step 1: cancel root context → monitor goroutine exits.
			cancelRoot()

			// Step 2: wait for clipboard service to stop (drains its
			// consumer goroutine and closes the events channel).
			// clipSvc.Stop() blocks until the goroutine is gone.
			clipSvc.Stop()

			// Step 3: close hotkey manager.
			if err := hotkeyMgr.Close(); err != nil {
				log.Warn("hotkey manager close error", "error", err)
			}

			// Step 4: close database.
			// All writers (the clipboard service) have already stopped.
			if err := db.Close(); err != nil {
				log.Warn("database close error", "error", err)
			}

			// Note: the image clipboard owner goroutine (held by
			// golang.design/x/clipboard inside clipSvc.imgWriter)
			// is released by clipSvc.Stop() above, which is why
			// no explicit clipboard teardown is needed here.

			// Step 5: remove the IPC socket so the next launch
			// doesn't see a stale socket.
			_ = os.Remove(sockPath)

			log.Info("shutdown complete")

			// Signal Fyne to quit its event loop.
			fyneApp.Quit()
		},
	}, version, log)

	// ── 13. Start IPC listener (for --popup forwarding) ───────────────
	go listenForPopupSignal(rootCtx, sockPath, popupWin, log)

	// ── 14. Start clipboard monitor ───────────────────────────────────
	clipSvc.Start(rootCtx)
	log.Info("clipboard monitor started")

	// Start popup event listener — reloads list when clips change.
	popupWin.StartListening(rootCtx)

	// Update tray tooltip with initial clip count.
	go func() {
		ctx, cancel := context.WithTimeout(rootCtx, 3*time.Second)
		defer cancel()
		if count, err := historyRepo.Count(ctx); err == nil {
			t.UpdateClipCount(count)
		}
	}()

	// Forward new clip events to tray count and popup.
	go func() {
		for range clipSvc.Subscribe() {

			ctx, cancel := context.WithTimeout(rootCtx, time.Second)

			count, err := historyRepo.Count(ctx)

			cancel()

			if err == nil {
				t.UpdateClipCount(count)
			}

			popupWin.NotifyClipChanged()
		}
	}()

	// ── 15. Register global hotkey ────────────────────────────────────
	//
	// Read the current shortcut from settings (falls back to default).
	ctx3s, cancel3s := context.WithTimeout(rootCtx, 3*time.Second)
	hotkeySetting, err := settingsSvc.Get(ctx3s, domain.KeyHotkey)
	cancel3s()
	if err != nil || hotkeySetting == "" {
		hotkeySetting = domain.DefaultSettings[domain.KeyHotkey]
	}
	currentHotkey = hotkeySetting

	if err := registerHotkey(hotkeySetting, func() {
		popupWin.Show()
	}); err != nil {
		log.Warn("failed to register global hotkey",
			"shortcut", hotkeySetting, "error", err)
		// Non-fatal: the app still works via the tray.
	} else {
		log.Info("global hotkey registered", "shortcut", hotkeySetting)
	}

	// If launched with --popup, show it immediately.
	if *showPopup {
		popupWin.Show()
	}

	// ── 16. Startup timing ────────────────────────────────────────────
	log.Debug("startup complete", "elapsed", time.Since(startTime))

	// ── 17. Fyne event loop (blocks until fyneApp.Quit()) ─────────────
	//
	// The tray must run in a goroutine because systray.Run() also blocks.
	// Fyne's event loop runs on the main goroutine as required.
	go t.Run()

	fyneApp.Run()

	log.Info("eruditto exited")
}

// ─────────────────────────────────────────────────────────────────────────────
// Single instance / IPC
// ─────────────────────────────────────────────────────────────────────────────

// tryForwardToExisting attempts to connect to a running instance via
// the Unix socket. If successful and showPopup is true, it sends a
// "popup" command and returns true (caller should exit).
// If the socket exists but is stale (no listener), removes it and
// returns false (caller starts fresh).
func tryForwardToExisting(sockPath string, showPopup bool, log *slog.Logger) bool {
	conn, err := net.DialTimeout("unix", sockPath, 500*time.Millisecond)
	if err != nil {
		// No running instance (or stale socket).
		if _, statErr := os.Stat(sockPath); statErr == nil {
			// Stale socket — remove it so we can bind to it.
			log.Debug("removing stale socket", "path", sockPath)
			_ = os.Remove(sockPath)
		}
		return false
	}
	defer conn.Close()

	// Running instance found.
	if showPopup {
		log.Info("forwarding --popup to running instance")
		_, _ = fmt.Fprintln(conn, "popup")
	} else {
		log.Info("eruditto is already running")
	}
	return true
}

// listenForPopupSignal listens on sockPath for IPC commands from
// subsequent instances launched with --popup.
//
// Protocol: single-line text commands over a Unix socket.
//   - "popup\n" → call popupWin.Show()
//
// The listener exits when ctx is cancelled (application shutdown).
func listenForPopupSignal(
	ctx context.Context,
	sockPath string,
	popup *ui.PopupWindow,
	log *slog.Logger,
) {
	// Remove any leftover socket from a previous run.
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		log.Warn("could not start IPC listener (--popup forwarding disabled)",
			"error", err)
		return
	}
	defer func() {
		ln.Close()
		os.Remove(sockPath)
	}()

	log.Debug("IPC listener ready", "path", sockPath)

	// Accept loop — each connection is handled synchronously (one
	// command per connection, very low volume).
	go func() {
		<-ctx.Done()
		ln.Close() // unblocks Accept()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Context cancelled — clean shutdown.
			return
		}
		go handleIPCConn(conn, popup, log)
	}
}

// handleIPCConn reads one command from conn and acts on it.
func handleIPCConn(conn net.Conn, popup *ui.PopupWindow, log *slog.Logger) {
	defer conn.Close()

	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return
	}

	cmd := string(buf[:n])
	// Trim newline from fmt.Fprintln in the sender.
	if len(cmd) > 0 && cmd[len(cmd)-1] == '\n' {
		cmd = cmd[:len(cmd)-1]
	}

	switch cmd {
	case "popup":
		log.Debug("IPC: received popup command")
		popup.Show()
	default:
		log.Warn("IPC: unknown command", "cmd", cmd)
	}
}
