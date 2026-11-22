//go:build linux

package sandbox

import "testing"

func TestHandledByABI(t *testing.T) {
	if handled(1)&fsRefer != 0 || handled(2)&fsRefer == 0 || handled(3)&fsTruncate == 0 || handled(4)&fsIoctlDev != 0 || handled(5)&fsIoctlDev == 0 {
		t.Fatal("handled rights do not follow the ABI")
	}
	if writeRights&fsExecute != 0 {
		t.Fatal("a write rule must not allow execute")
	}
}
