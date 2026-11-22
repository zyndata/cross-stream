package pep

import (
	"io"
	"net"
)

// Pipe copies between a stream and a TCP connection both ways; EOF on one
// side half-closes the other. An error on either side resets the stream and
// aborts conn (abort, e.g. an RST to the client; nil = conn.Close). It
// returns when both directions ended, with both closed.
func Pipe(st *Stream, conn net.Conn, abort func()) {
	if abort == nil {
		abort = func() { _ = conn.Close() }
	}
	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(st, conn)
		if err == nil {
			err = st.CloseWrite()
		}
		errc <- err
	}()
	go func() {
		_, err := io.Copy(conn, st)
		if err == nil {
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				err = cw.CloseWrite()
			}
		}
		errc <- err
	}()
	failed := false
	for range 2 {
		if err := <-errc; err != nil && !failed {
			failed = true
			st.Abort()
			abort()
		}
	}
	_ = st.Close()
	_ = conn.Close()
}
