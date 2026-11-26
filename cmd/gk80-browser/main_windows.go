package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
	"github.com/vitalcc55/gk80-control/internal/profile"
	"golang.org/x/sys/windows"
)

const appVersion = "0.1.0"
const browserAddress = "127.0.0.1:17880"

func main() {
	shared := flag.Bool("shared-profile", false, "use the desktop demo profile")
	flag.Parse()
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*shared); err != nil {
		report(os.Stderr, err)
		os.Exit(failureExitCode(err))
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

func run(shared bool) (result error) {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("browser location unavailable")
	}
	assets, err := os.OpenRoot(filepath.Join(filepath.Dir(executable), "browser-assets"))
	if err != nil {
		return errAssets
	}
	defer func() { _ = assets.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	listener, err := listen(ctx)
	if err != nil {
		return errPort
	}
	defer func() { _ = listener.Close() }()
	local := os.Getenv("LOCALAPPDATA")
	configured := os.Getenv("GK80_DEMO_PROFILE_DIR")
	var profileDir string
	profileID := profile.PreviewID
	mode := browser.Preview
	if shared {
		mode = browser.SharedDemo
		profileID = profile.DemoID
		profileDir, err = profile.DemoDir(local, configured)
	} else {
		profileDir, err = profile.PreviewDir(local, configured)
	}
	if err != nil {
		return errors.Join(errProfilePath, err)
	}
	hostID, err := uuid.NewRandom()
	if err != nil {
		return errors.New("browser identity unavailable")
	}
	logger, logErr := diagnostics.Open(filepath.Join(profileDir, "logs"), diagnostics.RoleBrowser, hostID, os.Stderr, diagnostics.Config{AppVersion: appVersion, ProfileID: profileID})
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
	owner := desktop.NewOwner(hostID, filepath.Join(filepath.Dir(executable), "gk80-core.exe"), profileDir, profileID, appVersion, logger.Application(diagnostics.ComponentTransport), logger.Degraded(), logErr != nil, func(contract.DesktopStatus) {})
	coreWasReady := false
	defer func() {
		if err := owner.Close(); err != nil {
			result = errors.Join(result, errOwnerClose, err)
		}
		if coreWasReady && !owner.CoreExitConfirmed() {
			result = errors.Join(result, errCoreExit)
		}
	}()
	host, err := browser.NewHost(listener, owner, assets.FS(), mode)
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
	_, _ = fmt.Fprintf(os.Stdout, "Браузерная демонстрация: http://%s%s\nПрофиль: %s (%s)\nДля штатного завершения введите exit.\n", browserAddress, mode.EntryPath(), profileID, profileDir)
	go readConsoleExit(os.Stdin, cancel)
	select {
	case <-ctx.Done():
		return nil
	case err := <-server:
		if ctx.Err() != nil && err == nil {
			return nil
		}
		return errors.Join(errHostRun, err)
	}
}
