//go:build !windows

package internal

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/term"
)

// WatchEscQuit opens /dev/tty in raw mode. On first ESC it shows a
// confirmation prompt; pressing ESC again (or 'y') within 3 seconds shuts
// down. Any other key cancels. Returns immediately if /dev/tty is unavailable.
func WatchEscQuit(onQuit func()) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return
	}

	fd := int(tty.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		tty.Close()
		return
	}

	w := func(s string) { fmt.Fprint(tty, s) }

	w("\r\n  Press ESC to stop the server.\r\n\r\n")

	go func() {
		defer tty.Close()
		defer term.Restore(fd, oldState)

		buf := make([]byte, 1)
		confirming := false
		var confirmDeadline time.Time

		for {
			// Use a short read deadline so we can expire the confirmation.
			if confirming {
				remaining := time.Until(confirmDeadline)
				if remaining <= 0 {
					w("\r  Cancelled.                                      \r\n")
					confirming = false
					continue
				}
			}

			n, err := tty.Read(buf)
			if err != nil || n == 0 {
				// Timeout or error — check if confirmation expired.
				if confirming && time.Now().After(confirmDeadline) {
					w("\r  Cancelled.                                      \r\n")
					confirming = false
				}
				continue
			}

			if confirming {
				if time.Now().After(confirmDeadline) {
					w("\r  Cancelled.                                      \r\n")
					confirming = false
					continue
				}
				if buf[0] == 0x1b || buf[0] == 'y' || buf[0] == 'Y' { // ESC or y
					w("\r\n  Shutting down...\r\n")
					onQuit()
					return
				}
				// Any other key cancels.
				w("\r  Cancelled.                                      \r\n")
				confirming = false
			} else if buf[0] == 0x1b {
				confirming = true
				confirmDeadline = time.Now().Add(3 * time.Second)
				w("\r  Stop server? Press ESC or 'y' to confirm, any other key to cancel.\r\n")
			}
		}
	}()
}
