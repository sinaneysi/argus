//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

// The Landlock system calls have the same numbers on every Linux architecture (they came after the table merge).
const (
	sysCreateRuleset = 444
	sysAddRule       = 445
	sysRestrictSelf  = 446

	createRulesetVersion = 1 << 0
	rulePathBeneath      = 1
	prSetNoNewPrivs      = 38
	oPath                = 0x200000 // O_PATH (the same value on every architecture Go supports, except sparc and alpha)
)

// Landlock file system access rights (include/uapi/linux/landlock.h).
const (
	fsExecute    = 1 << 0
	fsWriteFile  = 1 << 1
	fsReadFile   = 1 << 2
	fsReadDir    = 1 << 3
	fsRemoveDir  = 1 << 4
	fsRemoveFile = 1 << 5
	fsMakeChar   = 1 << 6
	fsMakeDir    = 1 << 7
	fsMakeReg    = 1 << 8
	fsMakeSock   = 1 << 9
	fsMakeFifo   = 1 << 10
	fsMakeBlock  = 1 << 11
	fsMakeSym    = 1 << 12
	fsRefer      = 1 << 13 // ABI 2
	fsTruncate   = 1 << 14 // ABI 3
	fsIoctlDev   = 1 << 15 // ABI 5

	fileRights = fsExecute | fsWriteFile | fsReadFile | fsTruncate | fsIoctlDev // the rights a rule on a file may hold
)

// handled is every file system right the kernel's ABI knows: all of them are denied unless a rule allows them.
func handled(abi int) uint64 {
	h := uint64(fsExecute | fsWriteFile | fsReadFile | fsReadDir | fsRemoveDir | fsRemoveFile | fsMakeChar | fsMakeDir |
		fsMakeReg | fsMakeSock | fsMakeFifo | fsMakeBlock | fsMakeSym)
	if abi >= 2 {
		h |= fsRefer
	}
	if abi >= 3 {
		h |= fsTruncate
	}
	if abi >= 5 {
		h |= fsIoctlDev
	}
	return h
}

// rights of each rule kind, before masking with handled.
const (
	readRights  = fsReadFile | fsReadDir
	execRights  = readRights | fsExecute
	writeRights = readRights | fsWriteFile | fsRemoveDir | fsRemoveFile | fsMakeDir | fsMakeReg | fsMakeSym | fsMakeFifo |
		fsMakeSock | fsRefer | fsTruncate | fsIoctlDev
)

type rulesetAttr struct{ handledFS uint64 }

// pathBeneathAttr is struct landlock_path_beneath_attr (packed: 12 bytes; Go pads the tail, the kernel reads 12).
type pathBeneathAttr struct {
	allowed  uint64
	parentFd int32
}

// ABI is the Landlock ABI version of the running kernel (0: not available).
func ABI() int {
	v, _, e := syscall.RawSyscall(sysCreateRuleset, 0, 0, createRulesetVersion)
	if e != 0 {
		return 0
	}
	return int(v)
}

// Restrict confines the calling OS thread (and every program it execs) to r. The caller must hold the thread
// (runtime.LockOSThread) and exec on it; other threads of the process are not confined.
func Restrict(r Rules) error {
	if err := r.check(); err != nil {
		return err
	}
	abi := ABI()
	if abi < 1 {
		return fmt.Errorf("%w (the kernel needs Landlock: Linux 5.13+ with landlock in the LSM list, and the container's seccomp profile must allow the landlock calls)", ErrUnavailable)
	}
	h := handled(abi)
	attr := rulesetAttr{handledFS: h}
	fd, _, e := syscall.RawSyscall(sysCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if e != 0 {
		return fmt.Errorf("%w: create ruleset: %v", ErrUnavailable, e)
	}
	defer syscall.Close(int(fd))
	for _, x := range []struct {
		paths  []string
		rights uint64
	}{{r.Exec, execRights}, {r.Read, readRights}, {r.Write, writeRights}} {
		for _, p := range x.paths {
			if err := addRule(int(fd), p, x.rights&h); err != nil {
				return err
			}
		}
	}
	if _, _, e := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); e != 0 {
		return fmt.Errorf("%w: no_new_privs: %v", ErrUnavailable, e)
	}
	if _, _, e := syscall.RawSyscall(sysRestrictSelf, fd, 0, 0); e != 0 {
		return fmt.Errorf("%w: restrict self: %v", ErrUnavailable, e)
	}
	return nil
}

// addRule allows rights beneath path. A path that does not exist is skipped (e.g. /lib64 on a musl system).
func addRule(ruleset int, path string, rights uint64) error {
	fd, err := syscall.Open(path, oPath|syscall.O_CLOEXEC, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: open %s: %v", ErrUnavailable, path, err)
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("%w: stat %s: %v", ErrUnavailable, path, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		rights &= fileRights
	}
	attr := pathBeneathAttr{allowed: rights, parentFd: int32(fd)}
	if _, _, e := syscall.RawSyscall6(sysAddRule, uintptr(ruleset), rulePathBeneath, uintptr(unsafe.Pointer(&attr)), 0, 0, 0); e != 0 {
		return fmt.Errorf("%w: rule for %s: %v", ErrUnavailable, path, e)
	}
	return nil
}

// Exec confines this thread to r and replaces the process with argv (looked up in PATH before the rules apply).
// It returns only on failure.
func Exec(r Rules, argv []string, env []string) error {
	prog, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	runtime.LockOSThread() // never unlocked: the thread execs or the process exits
	if err := Restrict(r); err != nil {
		return err
	}
	return syscall.Exec(prog, argv, env)
}
