package util

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *cancelOnCloseBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
}

// DoReadOnlyWithRetry retries a non-consuming GET within its original timeout.
// Do not use it for credited GETs, polling, or one-time-code endpoints.
func DoReadOnlyWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil || req == nil || req.Method != http.MethodGet {
		return nil, fmt.Errorf("read-only retry requires a GET request and HTTP client")
	}
	budget := client.Timeout
	if budget <= 0 {
		budget = 30 * time.Second
	}
	deadline := time.Now().Add(budget)
	if parentDeadline, ok := req.Context().Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		attemptCtx, cancel := context.WithDeadline(req.Context(), deadline)
		resp, err := client.Do(req.Clone(attemptCtx))
		if err != nil {
			if resp != nil && resp.Body != nil {
				_ = resp.Body.Close()
			}
			cancel()
			if attempt == 2 || !retryableReadTransport(err) {
				return nil, err
			}
		} else if attempt == 2 || !retryableReadStatus(resp.StatusCode) {
			resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
			return resp, nil
		} else {
			delay := readRetryDelay(attempt, resp)
			if delay >= time.Until(deadline) {
				resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
				return resp, nil
			}
			_ = resp.Body.Close()
			cancel()
			if err := waitReadRetry(req.Context(), delay); err != nil {
				return nil, err
			}
			continue
		}
		delay := jitterReadRetry(time.Duration(1<<attempt) * 500 * time.Millisecond)
		if delay >= time.Until(deadline) {
			return nil, err
		}
		if waitErr := waitReadRetry(req.Context(), delay); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, fmt.Errorf("read-only request exhausted retry budget")
}

func retryableReadStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func retryableReadTransport(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func readRetryDelay(attempt int, resp *http.Response) time.Duration {
	if resp.StatusCode == http.StatusTooManyRequests {
		value := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
			if seconds > int64((24*time.Hour)/time.Second) {
				return 24 * time.Hour
			}
			return time.Duration(seconds) * time.Second
		}
		if parsed, err := http.ParseTime(value); err == nil {
			if delay := time.Until(parsed); delay > 0 {
				return delay
			}
		}
	}
	return jitterReadRetry(time.Duration(1<<attempt) * 500 * time.Millisecond)
}

func jitterReadRetry(delay time.Duration) time.Duration {
	return delay * time.Duration(80+rand.Intn(41)) / 100
}

func waitReadRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
