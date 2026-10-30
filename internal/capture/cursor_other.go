//go:build !linux && !windows && !darwin

package capture

import (
	"context"
	"errors"
)

func WatchCursor(ctx context.Context, fn func(Cursor)) error {
	return errors.New("cursor watching is unsupported on this platform")
}
