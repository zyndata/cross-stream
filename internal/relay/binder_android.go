//go:build android && cgo

package relay

/*
#cgo LDFLAGS: -landroid
#include <android/multinetwork.h>
#include <errno.h>

static int xs_setsocknetwork(long long handle, int fd) {
	if (android_setsocknetwork((net_handle_t)handle, fd) != 0) {
		return errno;
	}
	return 0;
}
*/
import "C"

import "syscall"

// setSockNetwork binds fd to the Android network with this handle
// (android_setsocknetwork, API 23+).
func setSockNetwork(handle int64, fd uintptr) error {
	if e := C.xs_setsocknetwork(C.longlong(handle), C.int(fd)); e != 0 {
		return syscall.Errno(e)
	}
	return nil
}
