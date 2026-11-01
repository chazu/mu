//go:build unix

package staging

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSweepDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	victim := filepath.Join(outside, "precious")
	mkfile(t, victim, 1, now.Add(-48*time.Hour))
	link := filepath.Join(root, "mu-out-1")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-48 * time.Hour)
	tv := []unix.Timeval{unix.NsecToTimeval(old.UnixNano()), unix.NsecToTimeval(old.UnixNano())}
	if err := unix.Lutimes(link, tv); err != nil {
		t.Skipf("cannot set symlink times: %v", err)
	}
	if _, err := Sweep([]Rule{{Dir: root, Prefixes: []string{"mu-out-"}}}, MinAge, now, false); err != nil {
		t.Fatal(err)
	}
	if !exists(victim) {
		t.Fatal("sweep followed a symlink and deleted its target")
	}
}
