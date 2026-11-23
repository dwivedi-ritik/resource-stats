//go:build !windows

package dockerapi

import (
	"context"
	"errors"
	"net"
)

func dialPipe(ctx context.Context, path string) (net.Conn, error) {
	return nil, errors.New("named pipes are only available on Windows")
}
