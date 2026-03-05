package cmd

import (
	"io"
	"net"
)

// TCPForwarder listens on a local address and forwards connections to a target.
type TCPForwarder struct {
	ln     net.Listener
	target string
}

func NewTCPForwarder(listenAddr, target string) (*TCPForwarder, error) {
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, err
	}
	return &TCPForwarder{ln: ln, target: target}, nil
}

func (f *TCPForwarder) Addr() net.Addr {
	return f.ln.Addr()
}

func (f *TCPForwarder) Close() error {
	return f.ln.Close()
}

func (f *TCPForwarder) Serve() error {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return err
		}
		go f.forward(conn)
	}
}

func (f *TCPForwarder) forward(src net.Conn) {
	defer src.Close()
	dst, err := net.Dial("tcp", f.target)
	if err != nil {
		return
	}
	defer dst.Close()
	go func() {
		io.Copy(dst, src)
		dst.Close()
	}()
	io.Copy(src, dst)
}
