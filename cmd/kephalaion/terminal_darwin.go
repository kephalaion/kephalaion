package main

import "golang.org/x/sys/unix"

// isTerminal sagt, ob fd ein Terminal ist: nur dort kennt der Kernel die
// Einstellungen eines Terminals. /dev/null und eine Pipe sind keines.
func isTerminal(fd uintptr) bool {
	_, err := unix.IoctlGetTermios(int(fd), unix.TIOCGETA)
	return err == nil
}
