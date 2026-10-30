//go:build desktopapp

package main

import (
	"context"
	"embed"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/windows"
)

//go:embed all:dist-desktop
var desktopAssets embed.FS

const appVersion = "0.1.0"
const profileID = "demo-gk80-control"

func main() {
	if err := runSafely(); err != nil {
		showFatal(errors.Is(err, transport.ErrCleanupUnconfirmed))
		os.Exit(1)
	}
}

func showFatal(cleanupUnconfirmed bool) {
	text := "Не удалось запустить или корректно завершить приложение ГК80."
	if cleanupUnconfirmed {
		text += " Завершение ядра не подтверждено."
	}
	message, _ := windows.UTF16PtrFromString(text)
	title, _ := windows.UTF16PtrFromString("ГК80")
	_, _ = windows.MessageBox(0, message, title, windows.MB_OK|windows.MB_ICONERROR)
}

func showDiagnosticWarning() {
	message, _ := windows.UTF16PtrFromString("Журнал диагностики недоступен, неполон или его завершение не подтверждено. Состояние сохранения испытания определяется отдельно.")
	title, _ := windows.UTF16PtrFromString("ГК80 — диагностика")
	done := make(chan struct{})
	go func() {
		_, _ = windows.MessageBox(0, message, title, windows.MB_OK|windows.MB_ICONWARNING)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func runSafely() (err error) {
	defer func() {
		if recover() != nil {
			err = errors.Join(err, errors.New("desktop failure"))
		}
	}()
	return run()
}

func run() (resultErr error) {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("desktop location unavailable")
	}
	profileDir := os.Getenv("GK80_DEMO_PROFILE_DIR")
	if profileDir == "" {
		local := os.Getenv("LOCALAPPDATA")
		if !filepath.IsAbs(local) {
			return errors.New("local data directory unavailable")
		}
		profileDir = filepath.Join(local, "GK80", profileID)
	}
	if !filepath.IsAbs(profileDir) {
		return errors.New("invalid demo profile directory")
	}
	hostID, err := uuid.NewRandom()
	if err != nil {
		return errors.New("desktop identity unavailable")
	}
	logger, logErr := diagnostics.Open(filepath.Join(profileDir, "logs"), diagnostics.RoleDesktop, hostID, os.Stderr, diagnostics.Config{AppVersion: appVersion, ProfileID: profileID})
	if logger == nil {
		return errors.New("desktop diagnostics unavailable")
	}
	var owner *desktop.Owner
	defer func() {
		logger.Application(diagnostics.ComponentDesktop).Info(string(diagnostics.ProcessStopped))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeErr := logger.Close(ctx)
		if closeErr != nil || logger.Status().Degraded || (owner != nil && owner.Status().DiagnosticsDegraded) {
			showDiagnosticWarning()
		}
	}()
	logger.Application(diagnostics.ComponentDesktop).Info(string(diagnostics.ProcessStarted))
	var app *application.App
	owner = desktop.NewOwner(hostID, filepath.Join(filepath.Dir(executable), "gk80-core.exe"), profileDir, profileID, appVersion, logger.Application(diagnostics.ComponentTransport), logger.Degraded(), logErr != nil, func(status contract.DesktopStatus) {
		app.Event.Emit("desktop:status", status)
	})
	defer func() {
		panicValue := recover()
		if closeErr := owner.Close(); closeErr != nil {
			if errors.Is(closeErr, transport.ErrCleanupUnconfirmed) {
				logger.Application(diagnostics.ComponentDesktop).Error(string(diagnostics.CoreCleanupFailed))
			}
			resultErr = errors.Join(resultErr, closeErr)
		}
		if panicValue != nil {
			resultErr = errors.Join(resultErr, errors.New("desktop failure"))
		}
	}()
	app = application.New(application.Options{
		Name: "ГК80", Description: "Демонстрационное приложение ГК80",
		Services: []application.Service{desktop.NewBootstrapWailsService(owner), desktop.NewWailsService(owner)},
		Assets:   application.AssetOptions{Handler: application.AssetFileServerFS(desktopAssets)},
		Logger:   logger.Framework(),
		ErrorHandler: func(err error) {
			logger.Framework().Error("Wails failure")
			var fatal *application.FatalError
			if errors.As(err, &fatal) {
				closeErr := owner.Close()
				if errors.Is(closeErr, transport.ErrCleanupUnconfirmed) {
					logger.Application(diagnostics.ComponentDesktop).Error(string(diagnostics.CoreCleanupFailed))
				}
				logger.Application(diagnostics.ComponentDesktop).Error(string(diagnostics.ProcessStopped), "exit_code", 1)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				logCloseErr := logger.Close(ctx)
				cancel()
				if logCloseErr != nil || logger.Status().Degraded || owner.Status().DiagnosticsDegraded {
					showDiagnosticWarning()
				}
				showFatal(errors.Is(closeErr, transport.ErrCleanupUnconfirmed))
			}
		},
	})
	owner.Start()
	app.Window.NewWithOptions(application.WebviewWindowOptions{Title: "ГК80 — демонстрация", Width: 1200, Height: 800, URL: "/"})
	if err := app.Run(); err != nil {
		return errors.New("desktop startup or cleanup failed")
	}
	return nil
}
