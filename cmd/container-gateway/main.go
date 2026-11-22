// container-gateway is the image's PID 1. Business CLI tools remain available
// by passing their arguments or executing /app/tiku-adapter directly.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/itihey/tikuAdapter/internal/gateway"
)

const runtimeRoot = "/run/tiku-gateway"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 {
		if len(args) == 1 && args[0] == "--healthcheck" {
			return healthcheck()
		}
		command := exec.Command("/app/tiku-adapter", args...)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	}
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if raw == "" && strings.TrimSpace(os.Getenv("TRUSTED_PROXIES_FILE")) != "" {
		data, err := os.ReadFile(os.Getenv("TRUSTED_PROXIES_FILE"))
		if err != nil {
			return fmt.Errorf("read TRUSTED_PROXIES_FILE: %w", err)
		}
		raw = string(data)
	}
	proxyConfig, err := gateway.ProxyConfig(raw)
	if err != nil {
		return err
	}
	manager := &gateway.Manager{Root: runtimeRoot, CertificateDir: "/etc/nginx/ssl", ConfigDir: "/app/nginx-extra", QRCodePath: "/usr/share/nginx/html/Wechat_qc_code.jpg", ProxyConfig: proxyConfig, Report: func(s string) { log.Print(s) }}
	manager.Check = func(path string) error { return nginxCommand("-t", "-c", path) }
	if _, err = manager.Refresh(time.Now()); err != nil {
		// Restart during a renewal/config edit can still use the last accepted
		// private generation, after independently validating its complete config.
		if _, stateErr := gateway.CurrentTLSStatus(runtimeRoot, time.Now()); stateErr != nil {
			return err
		}
		if previousErr := manager.Check(manager.ActiveConfig()); previousErr != nil {
			return fmt.Errorf("candidate failed: %v; previous configuration invalid: %w", err, previousErr)
		}
		log.Printf("startup candidate rejected; using previous private configuration: %v", err)
	}
	// active.conf may remain after a container restart, but no master exists yet.
	manager.Reload = func() error { return reloadNginx(manager.ActiveConfig()) }
	backend := exec.Command("/app/tiku-adapter")
	backend.Env = append(os.Environ(), "TIKU_LISTEN_ADDRESS=127.0.0.1:8061", "TIKU_INTERNAL_GATEWAY=1")
	nginx := exec.Command("nginx", "-c", manager.ActiveConfig(), "-g", "daemon off;")
	for _, command := range []*exec.Cmd{backend, nginx} {
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
	defer stop()
	return supervise(ctx, backend, nginx, 3*time.Second, func() error { _, e := manager.Refresh(time.Now()); return e }, 10*time.Second)
}

func nginxCommand(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "nginx", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func nginxWorkers() (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(runtimeRoot, "nginx.pid"))
	if err != nil {
		return nil, err
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" || strings.ContainsAny(pid, "/\\") {
		return nil, errors.New("invalid Nginx PID")
	}
	data, err = os.ReadFile("/proc/" + pid + "/task/" + pid + "/children")
	if err != nil {
		return nil, err
	}
	result := map[string]bool{}
	for _, worker := range strings.Fields(string(data)) {
		result[worker] = true
	}
	return result, nil
}

// Nginx's signal command can succeed even when its master rejects a reload.
// A new worker generation confirms that the candidate was actually accepted.
func reloadNginx(config string) error {
	before, err := nginxWorkers()
	if err != nil {
		return err
	}
	if err = nginxCommand("-c", config, "-s", "reload"); err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		after, e := nginxWorkers()
		if e != nil {
			return e
		}
		for worker := range after {
			if !before[worker] {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("Nginx did not start a new worker generation after reload")
}

func healthcheck() error {
	if err := nginxCommand("-t", "-c", filepath.Join(runtimeRoot, "active.conf")); err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	for _, url := range []string{"http://127.0.0.1:8061/readyz", "http://127.0.0.1:8060/readyz"} {
		response, err := client.Get(url)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("health request %s: %s", url, response.Status)
		}
	}
	status, err := gateway.CurrentTLSStatus(runtimeRoot, time.Now())
	if err != nil {
		return err
	}
	fmt.Println(status.Message())
	return nil
}

func supervise(ctx context.Context, backend, nginx *exec.Cmd, interval time.Duration, refresh func() error, grace time.Duration) error {
	if err := backend.Start(); err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	backendDone := make(chan error, 1)
	go func() { backendDone <- backend.Wait() }()
	if err := nginx.Start(); err != nil {
		_ = backend.Process.Kill()
		<-backendDone
		return fmt.Errorf("start nginx: %w", err)
	}
	nginxDone := make(chan error, 1)
	go func() { nginxDone <- nginx.Wait() }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	backendExited, nginxExited := false, false
	stop := func() {
		if !nginxExited {
			if err := nginx.Process.Signal(syscall.SIGQUIT); err != nil {
				_ = nginx.Process.Kill()
			}
			select {
			case <-nginxDone:
			case <-time.After(grace):
				_ = nginx.Process.Kill()
				<-nginxDone
			}
		}
		if !backendExited {
			if err := backend.Process.Signal(syscall.SIGTERM); err != nil {
				_ = backend.Process.Kill()
			}
			select {
			case <-backendDone:
			case <-time.After(grace):
				_ = backend.Process.Kill()
				<-backendDone
			}
		}
	}
	defer stop()
	var lastError string
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-backendDone:
			backendExited = true
			return fmt.Errorf("backend exited: %v", err)
		case err := <-nginxDone:
			nginxExited = true
			return fmt.Errorf("nginx exited: %v", err)
		case <-ticker.C:
			if err := refresh(); err != nil {
				if err.Error() != lastError {
					log.Printf("reload skipped; previous configuration retained: %v", err)
					lastError = err.Error()
				}
			} else {
				lastError = ""
			}
		}
	}
}
