//go:build !linux && !windows

package netcfg

import (
	"context"
	"errors"
)

func netlinkEvents(context.Context) (<-chan struct{}, error) {
	return nil, errors.New("netlink: Linux only")
}
