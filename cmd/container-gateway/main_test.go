package main

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestGatewayChild(t *testing.T) {
	if os.Getenv("GATEWAY_TEST_CHILD") == "" {
		return
	}
	if os.Getenv("GATEWAY_TEST_CHILD") == "exit" {
		time.Sleep(100 * time.Millisecond)
		os.Exit(7)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestSupervisorStopsSiblingWhenEitherProcessExits(t *testing.T) {
	for _, exiting := range []string{"backend", "nginx"} {
		t.Run(exiting, func(t *testing.T) {
			child := func(name string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run=^TestGatewayChild$")
				mode := "wait"
				if name == exiting {
					mode = "exit"
				}
				cmd.Env = append(os.Environ(), "GATEWAY_TEST_CHILD="+mode)
				return cmd
			}
			backend, nginx := child("backend"), child("nginx")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err := supervise(ctx, backend, nginx, time.Hour, func() error { return nil }, 200*time.Millisecond)
			if err == nil || !strings.Contains(err.Error(), exiting) {
				t.Fatalf("expected %s failure, got %v", exiting, err)
			}
			if backend.ProcessState == nil || nginx.ProcessState == nil {
				t.Fatal("supervisor left child process running")
			}
		})
	}
}
