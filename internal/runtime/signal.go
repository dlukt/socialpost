package runtime

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func ContextWithSignals(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	return ctx, cancel
}
