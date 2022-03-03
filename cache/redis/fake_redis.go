// Copyright 2020 Google LLC. All Rights Reserved.

package redis

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strconv"
	"testing"
)

// FakeServer is a fake redis server for stress test.
type FakeServer struct {
	ln   net.Listener
	tb   testing.TB
	last []string
}

// NewFakeServer starts a new fake redis server.
func NewFakeServer(tb testing.TB) *FakeServer {
	ln, err := net.Listen("tcp", "")
	if err != nil {
		tb.Fatal(err)
	}
	s := &FakeServer{ln: ln, tb: tb}
	go s.serve()
	tb.Cleanup(func() { s.Close() })
	return s
}

// Addr returns address of the fake redis server.
func (s *FakeServer) Addr() net.Addr {
	return s.ln.Addr()
}

// Close shuts down the fake redis server.
func (s *FakeServer) Close() {
	s.ln.Close()
}

func (s *FakeServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *FakeServer) handle(conn net.Conn) {
	defer conn.Close()
	b := bufio.NewReader(conn)
	for {
		request, err := s.readRequest(b)
		if err != nil {
			return
		}
		s.last = request
		s.tb.Logf("request: %q", request)

		if len(request) > 0 && request[0] == "SET" {
			conn.Write([]byte("+OK\r\n"))
		} else {
			// assume GET
			conn.Write([]byte("$10\r\n0123456789\r\n"))
		}
	}
}

func (s *FakeServer) readRequest(r *bufio.Reader) ([]string, error) {
	var line []byte
	nline, _, err := r.ReadLine()
	if err != nil {
		return nil, err
	}
	line = append(line, nline...)
	if !bytes.HasPrefix(nline, []byte("*")) {
		return nil, err
	}
	// *<n> array
	n, err := strconv.Atoi(string(nline[1:]))
	if err != nil {
		return nil, fmt.Errorf("wrong array %q: %v", nline, err)
	}
	var request []string
	for i := 0; i < n; i++ {
		nline, _, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, '\n')
		line = append(line, nline...)
		if !bytes.HasPrefix(nline, []byte("$")) {
			continue
		}
		// $<n>\r\n<value>\r\n
		sz, err := strconv.Atoi(string(nline[1:]))
		if err != nil {
			return nil, fmt.Errorf("wrong bytes %q: %v", nline, err)
		}
		nline, _, err = r.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, '\n')
		line = append(line, nline...)
		if sz != len(nline) {
			return nil, fmt.Errorf("unexpected value sz=%d v=%q", sz, nline)
		}
		request = append(request, string(nline))
	}
	return request, nil
}

func (s *FakeServer) lastRequest() []string {
	return s.last
}
