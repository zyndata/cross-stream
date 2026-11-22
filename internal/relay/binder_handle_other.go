//go:build !android || !cgo

package relay

// setSockNetwork needs Android; other builds use DeviceBinder or NoBinder.
func setSockNetwork(int64, uintptr) error { return ErrNotSupported }
