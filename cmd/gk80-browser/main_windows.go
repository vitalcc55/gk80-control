package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/vitalcc55/gk80-control/internal/browser"
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
	"github.com/vitalcc55/gk80-control/internal/transport"
	"golang.org/x/sys/windows"
)

const profileID = "demo-gk80-control"
const appVersion = "0.1.0"
const browserAddress = "127.0.0.1:17880"

var errPort = errors.New("browser address unavailable")
var errProfile = errors.New("demo profile in use")
var errCore = errors.New("demo core unavailable or incompatible")
var errPairWindow = errors.New("pairing window unavailable")
var errAssets = errors.New("browser assets unavailable")
var errHostShutdown = errors.New("browser host shutdown unconfirmed")
var errDiagnostics = errors.New("browser diagnostics unconfirmed")
var errCoreExit = errors.New("core exit unconfirmed")
var errOwnerClose = errors.New("owner shutdown unconfirmed")
var errHostRun = errors.New("browser host stopped unexpectedly")

func main() {
	if err := run(); err != nil {
		report(os.Stderr, err)
		os.Exit(1)
	}
}

func report(output io.Writer, err error) {
	reported := false
	for _, item := range []struct {
		target error
		text   string
	}{
		{errPort, "Порт браузерной демонстрации занят или недоступен."},
		{errProfile, "Демонстрационный профиль уже открыт другим приложением."},
		{errCore, "Демонстрационное ядро недоступно или несовместимо."},
		{errPairWindow, "Не удалось показать окно сопряжения."},
		{errAssets, "Ресурсы браузерной демонстрации не подготовлены."},
		{errHostShutdown, "Завершение браузерного host не подтверждено."},
		{errDiagnostics, "Журнал диагностики недоступен или неполон."},
		{errCoreExit, "Выход принадлежащего приложению ядра не подтверждён."},
		{errOwnerClose, "Корректное завершение сеанса или ядра не подтверждено."},
		{errHostRun, "Браузерный host остановился с ошибкой."},
		{transport.ErrCleanupUnconfirmed, "Выход принадлежащего приложению ядра не подтверждён."},
		{desktop.ErrSessionShutdownUnconfirmed, "Завершение сеанса не подтверждено; проверьте историю испытания."},
	} {
		if errors.Is(err, item.target) {
			_, _ = fmt.Fprintln(output, item.text)
			reported = true
		}
	}
	if !reported {
		_, _ = fmt.Fprintln(output, "Браузерная демонстрация не запущена или её завершение не подтверждено. Проверьте журнал и состояние ядра.")
	}
}

func listen(ctx context.Context) (net.Listener, error) {
	config := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var socketErr error
		err := raw.Control(func(fd uintptr) {
			socketErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, ^windows.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return socketErr
	}}
	return config.Listen(ctx, "tcp4", browserAddress)
}

func showPairing(code string) error {
	message, err := windows.UTF16PtrFromString("Одноразовый код для браузерной демонстрации:\n\n" + code + "\n\nВведите его в открытой странице. Код действует две минуты.")
	if err != nil {
		return errPairWindow
	}
	title, err := windows.UTF16PtrFromString("ГК80 — сопряжение")
	if err != nil {
		return errPairWindow
	}
	if _, err := windows.MessageBox(0, message, title, windows.MB_OK|windows.MB_ICONINFORMATION); err != nil {
		return errPairWindow
	}
	return nil
}

func waitReady(ctx context.Context, owner *desktop.Owner) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		status := owner.Status()
		if status.Status == contract.DesktopStatusStatusReady && status.Handshake != nil {
			return nil
		}
		if status.Status == contract.DesktopStatusStatusProfileInUse {
			return errProfile
		}
		if status.Status != contract.DesktopStatusStatusStarting {
			return errCore
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return ctx.Err()
			}
			return errors.Join(errCore, ctx.Err())
		case <-ticker.C:
		}
	}
}

func run() (result error) {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("browser location unavailable")
	}
	profile := os.Getenv("GK80_DEMO_PROFILE_DIR")
	if profile == "" {
		local := os.Getenv("LOCALAPPDATA")
		if !filepath.IsAbs(local) {
			return errors.New("local data directory unavailable")
		}
		profile = filepath.Join(local, "GK80", profileID)
	}
	if !filepath.IsAbs(profile) {
		return errors.New("invalid demo profile")
	}
	assets, err := os.OpenRoot(filepath.Join(filepath.Dir(executable), "browser-assets"))
	if err != nil {
		return errAssets
	}
	defer func() { _ = assets.Close() }()
	listener, err := listen(context.Background())
	if err != nil {
		return errPort
	}
	defer func() { _ = listener.Close() }()
	hostID, err := uuid.NewRandom()
	if err != nil {
		return errors.New("browser identity unavailable")
	}
	logger, logErr := diagnostics.Open(filepath.Join(profile, "logs"), diagnostics.RoleBrowser, hostID, os.Stderr, diagnostics.Config{AppVersion: appVersion, ProfileID: profileID})
	if logger == nil {
		return errors.New("browser diagnostics unavailable")
	}
	defer func() {
		logger.Application(diagnostics.ComponentBrowser).Info(string(diagnostics.ProcessStopped))
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := logger.Close(ctx); err != nil || logger.Status().Degraded {
			result = errors.Join(result, errDiagnostics)
		}
	}()
	logger.Application(diagnostics.ComponentBrowser).Info(string(diagnostics.ProcessStarted))
	owner := desktop.NewOwner(hostID, filepath.Join(filepath.Dir(executable), "gk80-core.exe"), profile, profileID, appVersion, logger.Application(diagnostics.ComponentTransport), logger.Degraded(), logErr != nil, func(contract.DesktopStatus) {})
	coreWasReady := false
	defer func() {
		if err := owner.Close(); err != nil {
			result = errors.Join(result, errOwnerClose, err)
		}
		if coreWasReady && !owner.CoreExitConfirmed() {
			result = errors.Join(result, errCoreExit)
		}
	}()
	host, code, err := browser.NewHost(listener, owner, assets.FS(), 2*time.Minute, 8*time.Hour)
	if err != nil {
		if errors.Is(err, browser.ErrAssetsUnavailable) {
			return errors.Join(errAssets, err)
		}
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if err := host.Shutdown(ctx); err != nil {
			result = errors.Join(result, errHostShutdown, err)
		}
	}()
	owner.Start()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	readyCtx, stopReady := context.WithTimeout(ctx, 15*time.Second)
	err = waitReady(readyCtx, owner)
	stopReady()
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return err
	}
	coreWasReady = true
	server := make(chan error, 1)
	go func() { server <- host.Serve() }()
	_, _ = fmt.Fprintf(os.Stdout, "Браузерная демонстрация: http://%s\nПрофиль: %s (%s)\n", browserAddress, profileID, profile)
	pairing := make(chan error, 1)
	go func() { pairing <- showPairing(code) }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-server:
			if err != nil {
				return errors.Join(errHostRun, err)
			}
			return nil
		case err := <-pairing:
			if err != nil {
				return err
			}
			pairing = nil
		}
	}
}
