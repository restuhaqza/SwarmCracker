//go:build linux

package main

import "golang.org/x/sys/unix"

// makeRaw puts the terminal referred to by fd into raw mode. It returns a
// restore function (always safe to call) and whether raw mode was applied; a
// non-terminal fd (for example a pipe) is left untouched and reported as false.
func makeRaw(fd int) (restore func(), ok bool) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return func() {}, false
	}

	raw := *old
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return func() {}, false
	}

	return func() {
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, old)
	}, true
}
