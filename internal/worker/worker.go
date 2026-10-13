package worker

import (
	"context"
	"fmt"
)

func RunOnce(ctx context.Context, name string) string {
	select {
	case <-ctx.Done():
		return "cancelled"
	default:
		return fmt.Sprintf("%s: ok", name)
	}
}
