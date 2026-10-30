package core

import (
	"context"

	"github.com/vitalcc55/gk80-control/internal/demo"
	"github.com/vitalcc55/gk80-control/internal/diagnostics"
)

func startCommandLogging(engine *demo.Engine, handler *coreHTTP, logger *diagnostics.Logger) (context.CancelFunc, <-chan struct{}) {
	events, drops := engine.CommandEvents()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		logEvent := func(event demo.CommandEvent) {
			if event.ActionID != nil {
				handler.logCommand(event.Status, *event.ActionID)
			} else {
				handler.logCommand(event.Status)
			}
		}
		for {
			select {
			case event := <-events:
				logEvent(event)
			case <-drops:
				info, failed := engine.TakeCommandDrops()
				logger.ReportExternalDrops(info, failed)
			case <-ctx.Done():
				for {
					select {
					case event := <-events:
						logEvent(event)
					case <-drops:
						info, failed := engine.TakeCommandDrops()
						logger.ReportExternalDrops(info, failed)
					default:
						return
					}
				}
			}
		}
	}()
	return cancel, done
}
