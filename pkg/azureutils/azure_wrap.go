package azureutils

import (
	"context"
)

func getContextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
