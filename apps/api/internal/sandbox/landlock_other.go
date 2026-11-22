//go:build !linux

package sandbox

// ABI is 0: Landlock exists on Linux only.
func ABI() int { return 0 }

// Restrict always fails off Linux.
func Restrict(Rules) error { return ErrUnavailable }

// Exec always fails off Linux: the program is never run unconfined.
func Exec(Rules, []string, []string) error { return ErrUnavailable }
