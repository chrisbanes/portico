// Command peerprobe is the other user in the ipc cross-user tests. Run as
// another account, or root, it connects as a client or serves one connection,
// and reports how many bytes the other side sent it.
//
//	peerprobe client <socket>
//	peerprobe server <socket>
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: peerprobe client|server <socket>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "client":
		err = client(os.Args[2])
	case "server":
		err = server(os.Args[2])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func client(socket string) error {
	c, err := net.Dial("unix", socket)
	if err != nil {
		return err
	}
	defer c.Close()
	return report(c)
}

func server(socket string) error {
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(socket, 0o777); err != nil {
		return err
	}
	fmt.Println("ready")
	c, err := ln.Accept()
	if err != nil {
		return err
	}
	defer c.Close()
	return report(c)
}

// report prints the bytes received until the peer closes, or until a
// deadline that a peer which neither sends nor closes would hit.
func report(c net.Conn) error {
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	n, err := io.Copy(io.Discard, c)
	fmt.Printf("bytes=%d closed=%t\n", n, err == nil)
	return nil
}
