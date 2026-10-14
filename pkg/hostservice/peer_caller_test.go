package hostservice

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPeerCallerUsesKernelProcessIdentity(t *testing.T) {
	directory, err := os.MkdirTemp("", "concierge-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "peer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPeerCallerChild$")
	command.Env = append(os.Environ(), "CONCIERGE_TEST_PEER_SOCKET="+path)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var forged struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(connection).Decode(&forged); err != nil {
		t.Fatal(err)
	}
	caller, err := peerCaller(connection)
	if err != nil {
		t.Fatal(err)
	}
	if caller.PID != command.Process.Pid || caller.UID != uint32(os.Geteuid()) ||
		caller.PID == forged.PID {
		t.Fatalf("peer identity=%+v; child=%d; forged=%d", caller, command.Process.Pid, forged.PID)
	}
	if _, err := connection.Write([]byte("done\n")); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestPeerCallerChild(t *testing.T) {
	path := os.Getenv("CONCIERGE_TEST_PEER_SOCKET")
	if path == "" {
		t.Skip("child process only")
	}
	connection, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(connection).Encode(map[string]int{"pid": 1}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 5)
	if _, err := connection.Read(buffer); err != nil {
		t.Fatal(err)
	}
}
