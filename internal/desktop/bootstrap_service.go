package desktop

import (
	"context"

	"github.com/vitalcc55/gk80-control/internal/contract"
)

// BootstrapService exposes only the passive desktop handshake.
type BootstrapService struct{ owner *Owner }

func NewBootstrapService(owner *Owner) *BootstrapService { return &BootstrapService{owner: owner} }

func (s *BootstrapService) Status() contract.DesktopStatus { return s.owner.Status() }

func (s *BootstrapService) Verify(ctx context.Context) contract.DesktopStatus {
	return s.owner.Verify(ctx)
}
