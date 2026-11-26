package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
	"github.com/vitalcc55/gk80-control/internal/profile"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

var errPort = errors.New("browser address unavailable")
var errProfile = errors.New("demo profile in use")
var errProfilePath = errors.New("demo profile path unavailable")
var errCore = errors.New("demo core unavailable or incompatible")
var errAssets = errors.New("browser assets unavailable")
var errHostShutdown = errors.New("browser host shutdown unconfirmed")
var errDiagnostics = errors.New("browser diagnostics unconfirmed")
var errCoreExit = errors.New("core exit unconfirmed")
var errOwnerClose = errors.New("owner shutdown unconfirmed")
var errHostRun = errors.New("browser host stopped unexpectedly")

// The launcher can remove a failed startup's build directory only after cleanup is confirmed.
func failureExitCode(err error) int {
	if errors.Is(err, errOwnerClose) || errors.Is(err, errCoreExit) ||
		errors.Is(err, errHostShutdown) || errors.Is(err, transport.ErrCleanupUnconfirmed) {
		return 1
	}
	if errors.Is(err, errPort) || errors.Is(err, errProfilePath) || errors.Is(err, errProfile) ||
		errors.Is(err, errCore) || errors.Is(err, errAssets) {
		return 2
	}
	return 1
}

// stdin may be unavailable under a launcher. Only the explicit command ends the session.
func readConsoleExit(input io.Reader, stop context.CancelFunc) {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "exit" {
			stop()
			return
		}
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
		{profile.ErrSeparateProfiles, "Профиль IAB preview и профиль Windows-приложения должны находиться в разных каталогах и использовать разные базы."},
		{errProfilePath, "Проверьте доступность профиля и абсолютный путь к нему."},
		{errCore, "Демонстрационное ядро недоступно или несовместимо."},
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
