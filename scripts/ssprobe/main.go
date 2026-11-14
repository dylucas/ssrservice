// Command ssprobe is a small Shadowsocks client used to verify a running
// sspanel deployment end to end.
//
// Examples:
//
//	ssprobe -server 127.0.0.1:8388 -method 2022-blake3-aes-128-gcm \
//	  -server-psk <base64> -client-psk <base64> -target echo
//
//	ssprobe -server node.example.com:8388 -method aes-256-gcm \
//	  -client-psk password -target www.example.com:80 -request "GET / HTTP/1.0\r\nHost: www.example.com\r\n\r\n"
package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	M "github.com/sagernet/sing/common/metadata"
)

func main() {
	var (
		serverAddr = flag.String("server", "127.0.0.1:8388", "Shadowsocks server address")
		method     = flag.String("method", "2022-blake3-aes-128-gcm", "encryption method")
		serverPSK  = flag.String("server-psk", "", "server PSK for SS2022 multi-user methods")
		clientPSK  = flag.String("client-psk", "", "client PSK / password")
		target     = flag.String("target", "echo", "destination host:port, or 'echo' for a built-in echo server")
		request    = flag.String("request", "", "data to send (default: a probe message)")
		timeout    = flag.Duration("timeout", 10*time.Second, "overall timeout")
		udp        = flag.Bool("udp", false, "test UDP instead of TCP")
	)
	flag.Parse()

	if *clientPSK == "" {
		fail("client-psk is required")
	}
	if err := run(*serverAddr, *method, *serverPSK, *clientPSK, *target, *request, *timeout, *udp); err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "error:", message)
	os.Exit(1)
}

func run(serverAddr, method, serverPSK, clientPSK, target, request string, timeout time.Duration, useUDP bool) error {
	clientMethod, err := buildMethod(method, serverPSK, clientPSK)
	if err != nil {
		return err
	}

	if target == "echo" {
		addr, stop, err := startEcho(timeout)
		if err != nil {
			return err
		}
		defer stop()
		target = addr
	}
	if target == "udp-echo" {
		addr, stop, err := startUDPEcho()
		if err != nil {
			return err
		}
		defer stop()
		target = addr
		useUDP = true
	}

	destination := M.ParseSocksaddr(target)
	if !destination.IsValid() {
		return fmt.Errorf("invalid target %q", target)
	}
	if request == "" {
		request = "sspanel-probe " + time.Now().Format(time.RFC3339Nano)
	}

	if useUDP {
		return runUDP(clientMethod, serverAddr, destination, []byte(request), timeout)
	}

	raw, err := net.DialTimeout("tcp", serverAddr, timeout)
	if err != nil {
		return fmt.Errorf("dial server: %w", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(timeout))
	conn, err := clientMethod.DialConn(raw, destination)
	if err != nil {
		return fmt.Errorf("shadowsocks handshake: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write([]byte(request)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	response := make([]byte, 64*1024)
	n, err := conn.Read(response)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read: %w", err)
	}
	fmt.Printf("TCP OK: %s -> %s, sent %d bytes, received %d bytes\n", serverAddr, target, len(request), n)
	if n > 0 {
		fmt.Printf("response: %s\n", strings.TrimSpace(string(response[:n])))
	}
	return nil
}

func runUDP(clientMethod shadowsocks.Method, serverAddr string, destination M.Socksaddr, payload []byte, timeout time.Duration) error {
	raw, err := net.DialTimeout("udp", serverAddr, timeout)
	if err != nil {
		return fmt.Errorf("dial udp: %w", err)
	}
	defer raw.Close()
	conn := clientMethod.DialPacketConn(raw)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	if _, err := conn.WriteTo(payload, destination.UDPAddr()); err != nil {
		return fmt.Errorf("write udp: %w", err)
	}
	buffer := make([]byte, 64*1024)
	n, addr, err := conn.ReadFrom(buffer)
	if err != nil {
		return fmt.Errorf("read udp: %w", err)
	}
	fmt.Printf("UDP OK: %s -> %s, sent %d bytes, received %d bytes from %s\n", serverAddr, destination, len(payload), n, addr)
	return nil
}

func buildMethod(method, serverPSK, clientPSK string) (shadowsocks.Method, error) {
	if strings.HasPrefix(method, "2022-") {
		if serverPSK != "" {
			serverKey, err := base64.StdEncoding.DecodeString(serverPSK)
			if err != nil {
				return nil, fmt.Errorf("decode server-psk: %w", err)
			}
			userKey, err := base64.StdEncoding.DecodeString(clientPSK)
			if err != nil {
				return nil, fmt.Errorf("decode client-psk: %w", err)
			}
			return shadowaead_2022.New(method, [][]byte{serverKey, userKey}, time.Now)
		}
		return shadowaead_2022.NewWithPassword(method, clientPSK, time.Now)
	}
	return shadowaead.New(method, nil, clientPSK)
}

// startUDPEcho starts a loopback UDP echo server for self-contained tests.
func startUDPEcho() (string, func(), error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return "", nil, fmt.Errorf("start udp echo server: %w", err)
	}
	go func() {
		buf := make([]byte, 64*1024)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buf[:n], addr)
		}
	}()
	stop := func() { _ = conn.Close() }
	return conn.LocalAddr().String(), stop, nil
}

// startEcho starts a loopback TCP echo server for self-contained tests.
func startEcho(timeout time.Duration) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("start echo server: %w", err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	stop := func() { _ = listener.Close() }
	return listener.Addr().String(), stop, nil
}
