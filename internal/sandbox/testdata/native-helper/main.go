// A static Go executable for native sandbox acceptance. It avoids requiring
// host shells or dynamic loaders inside the Linux root filesystem.
package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "work":
		if err := os.WriteFile("native-output", []byte("ok"), 0o644); err != nil {
			fail(err)
		}
	case "write-denied":
		if len(os.Args) != 3 {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Args[2], []byte("escaped"), 0o644); err == nil {
			fail(fmt.Errorf("write allowed"))
		}
	case "read-denied":
		if len(os.Args) != 3 {
			os.Exit(2)
		}
		if _, err := os.ReadFile(os.Args[2]); err == nil {
			fail(fmt.Errorf("read allowed"))
		}
	case "network":
		if len(os.Args) != 4 {
			os.Exit(2)
		}
		conn, err := net.DialTimeout("tcp", os.Args[2], time.Second)
		if conn != nil {
			conn.Close()
		}
		allowed := os.Args[3] == "allow"
		if (err == nil) != allowed {
			fail(fmt.Errorf("network allow=%v: %v", allowed, err))
		}
	default:
		os.Exit(2)
	}
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
