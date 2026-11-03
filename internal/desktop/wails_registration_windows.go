//go:build desktopapp

package desktop

import (
	"github.com/vitalcc55/gk80-control/internal/contract"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// NewWailsService registers the domain service for the desktop application.
func NewWailsService(core Core) application.Service {
	return application.NewService(NewService(core))
}

func NewBootstrapWailsService(owner *Owner) application.Service {
	return application.NewService(NewBootstrapService(owner))
}

func init() {
	application.RegisterEvent[contract.Event]("core:event")
	application.RegisterEvent[contract.ConnectionState]("core:connection")
	application.RegisterEvent[contract.DesktopStatus]("desktop:status")
}
