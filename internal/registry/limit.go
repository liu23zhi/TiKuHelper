package registry

import (
	"github.com/itihey/tikuAdapter/configs"
	"github.com/itihey/tikuAdapter/pkg/ratelimit"
	"golang.org/x/time/rate"
)

// Limit get ratelimit instance
func Limit(cfg configs.Config) *ratelimit.IPRateLimiter {
	limit := cfg.Limit
	duration := limit.Duration
	requests := int(limit.Requests)
	if duration == 0 {
		duration = 60
	}
	if requests <= 0 {
		requests = 120
	}
	r := rate.Limit(float64(requests) / float64(duration))
	return ratelimit.NewIPRateLimiter(r, requests)
}
