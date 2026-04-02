package firecracker

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

func TestResolveGuestCIDAvoidsInUseCandidates(t *testing.T) {
	baseDir := t.TempDir()

	persistedVMDir := filepath.Join(baseDir, "persisted")
	if err := os.MkdirAll(persistedVMDir, 0755); err != nil {
		t.Fatalf("mkdir persisted vm dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(persistedVMDir, "guest.cid"), []byte("1500\n"), 0644); err != nil {
		t.Fatalf("write persisted cid: %v", err)
	}

	p := &Provider{
		config: Config{
			BaseDir: baseDir,
			CIDMin:  1024,
			CIDMax:  2048,
		},
		vms: map[string]*VMInstance{
			"running": {ID: "running", GuestCID: 1600},
		},
	}

	cid, err := p.resolveGuestCID("new-vm", 0)
	if err != nil {
		t.Fatalf("resolveGuestCID failed: %v", err)
	}
	if cid == 1500 || cid == 1600 {
		t.Fatalf("expected allocator to skip in-use cids, got %d", cid)
	}

	if _, err := p.resolveGuestCID("new-vm-2", 1500); err == nil {
		t.Fatal("expected requested cid collision error")
	}
}

func TestExecuteViaVsockSuccess(t *testing.T) {
	baseDir := t.TempDir()
	sockPath := filepath.Join(baseDir, "agent.vsock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			done <- fmt.Errorf("read connect line: %w", err)
			return
		}
		if strings.TrimSpace(line) != "CONNECT 10789" {
			done <- fmt.Errorf("unexpected connect line: %q", line)
			return
		}
		if _, err := conn.Write([]byte("OK 12345\n")); err != nil {
			done <- fmt.Errorf("write connect ack: %w", err)
			return
		}

		var req execRequest
		if err := readFramedJSON(reader, 1024*1024, &req); err != nil {
			done <- fmt.Errorf("read request frame: %w", err)
			return
		}

		resp := execResponse{
			ProtocolVersion: execProtocolVersion,
			RequestID:       req.RequestID,
			Status:          ExecProtocolStatusOK,
			ExitCode:        0,
			Stdout:          "ok\n",
			Stderr:          "",
		}
		if err := writeFramedJSON(conn, resp, 1024*1024); err != nil {
			done <- fmt.Errorf("write response frame: %w", err)
			return
		}

		done <- nil
	}()

	p := &Provider{
		config: Config{
			DefaultExecTimeout: 5 * time.Second,
			MaxStdoutBytes:     1024,
			MaxStderrBytes:     1024,
		},
	}

	vm := &VMInstance{
		ID:        "vm-1",
		VsockPath: sockPath,
		GuestCID:  1300,
		GuestPort: 10789,
		Config: vmdomain.CreateSpec{
			Runtime: vmdomain.RuntimeConfig{
				Exec: vmdomain.ExecLimits{
					Timeout:        5 * time.Second,
					MaxStdoutBytes: 1024,
					MaxStderrBytes: 1024,
				},
			},
		},
	}

	stdout, stderr, exitCode, execErr := p.executeViaVsock(context.Background(), vm, []string{"echo", "ok"})
	if execErr != nil {
		t.Fatalf("executeViaVsock failed: %v", execErr)
	}
	if stdout != "ok\n" {
		t.Fatalf("unexpected stdout %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr %q", stderr)
	}
	if exitCode != 0 {
		t.Fatalf("unexpected exit code %d", exitCode)
	}

	if err := <-done; err != nil {
		t.Fatalf("server goroutine failed: %v", err)
	}
}
