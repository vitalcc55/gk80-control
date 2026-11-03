package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/vitalcc55/gk80-control/internal/desktop"
	"github.com/vitalcc55/gk80-control/internal/transport"
)

var errPort = errors.New("browser address unavailable")
var errProfile = errors.New("demo profile in use")
var errCore = errors.New("demo core unavailable or incompatible")
var errAssets = errors.New("browser assets unavailable")
var errHostShutdown = errors.New("browser host shutdown unconfirmed")
var errDiagnostics = errors.New("browser diagnostics unconfirmed")
var errCoreExit = errors.New("core exit unconfirmed")
var errOwnerClose = errors.New("owner shutdown unconfirmed")
var errHostRun = errors.New("browser host stopped unexpectedly")

func report(output io.Writer, err error) {
	reported := false
	for _, item := range []struct {
		target error
		text   string
	}{
		{errPort, "Порт браузерной демонстрации занят или недоступен."},
		{errProfile, "Демонстрационный профиль уже открыт другим приложением."},
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
