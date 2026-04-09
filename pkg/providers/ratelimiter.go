package providers

import (
	"context"
	"sync"
	"time"
)

// RateLimiter implements a token-bucket rate limiter for a single key.
// Allows up to RPM requests per minute with a burst equal to RPM.
// Thread-safe.
type RateLimiter struct {
	mu       sync.Mutex
	rpm      int
	tokens   float64
	maxBurst float64
	lastTick time.Time
	nowFunc  func() time.Time // for testing
}

func (rl *RateLimiter) refillLocked(now time.Time) {
	elapsed := now.Sub(rl.lastTick).Seconds()
	rl.lastTick = now

	// Refill tokens proportional to elapsed time.
	refill := elapsed * float64(rl.rpm) / 60.0
	rl.tokens = min(rl.maxBurst, rl.tokens+refill)
}

// newRateLimiter creates a RateLimiter that allows rpm requests/minute.
func newRateLimiter(rpm int) *RateLimiter {
	return &RateLimiter{
		rpm:      rpm,
		tokens:   float64(rpm), // start full
		maxBurst: float64(rpm),
		lastTick: time.Now(),
		nowFunc:  time.Now,
	}
}

// Wait blocks until a token is available or ctx is canceled.
// Returns ctx.Err() if canceled while waiting.
func (rl *RateLimiter) Wait(ctx context.Context) error {
	for {
		rl.mu.Lock()
		now := rl.nowFunc()
		rl.refillLocked(now)

		if rl.tokens >= 1.0 {
			rl.tokens--
			rl.mu.Unlock()
			return nil
		}

		// Calculate how long until a token is available.
		deficit := 1.0 - rl.tokens
		waitSec := deficit / (float64(rl.rpm) / 60.0)
		rl.mu.Unlock()

		timer := time.NewTimer(time.Duration(waitSec * float64(time.Second)))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
			// Loop to re-check (another goroutine may have consumed the token).
		}
	}
}

// TryAcquire attempts to consume a token without blocking.
func (rl *RateLimiter) TryAcquire() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.refillLocked(rl.nowFunc())
	if rl.tokens < 1.0 {
		return false
	}
	rl.tokens--
	return true
}

// TPMRateLimiter implements a token-bucket rate limiter based on tokens per minute.
// Unlike RPM which checks before requests, TPM tracks actual token consumption
// and blocks when the budget is exhausted. Thread-safe.
type TPMRateLimiter struct {
	mu       sync.Mutex
	tpm      int     // tokens per minute limit
	tokens   float64 // available token budget
	maxBurst float64
	lastTick time.Time
	nowFunc  func() time.Time // for testing
}

func newTPMRateLimiter(tpm int) *TPMRateLimiter {
	return &TPMRateLimiter{
		tpm:      tpm,
		tokens:   float64(tpm), // start full
		maxBurst: float64(tpm),
		lastTick: time.Now(),
		nowFunc:  time.Now,
	}
}

func (tl *TPMRateLimiter) refillLocked(now time.Time) {
	elapsed := now.Sub(tl.lastTick).Seconds()
	tl.lastTick = now
	refill := elapsed * float64(tl.tpm) / 60.0
	tl.tokens = min(tl.maxBurst, tl.tokens+refill)
}

// TryConsume checks if the estimated token cost can be afforded right now.
// It does NOT deduct tokens — call Record after the API response.
// Returns true if the current budget can cover the estimate.
func (tl *TPMRateLimiter) TryConsume(estimate int) bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.refillLocked(tl.nowFunc())
	return tl.tokens >= float64(estimate)
}

// Wait blocks until enough tokens are available for the estimated cost.
func (tl *TPMRateLimiter) Wait(ctx context.Context, estimate int) error {
	for {
		tl.mu.Lock()
		now := tl.nowFunc()
		tl.refillLocked(now)

		if tl.tokens >= float64(estimate) {
			tl.mu.Unlock()
			return nil
		}

		// Calculate wait time until enough tokens refill.
		deficit := float64(estimate) - tl.tokens
		waitSec := deficit / (float64(tl.tpm) / 60.0)
		tl.mu.Unlock()

		timer := time.NewTimer(time.Duration(waitSec * float64(time.Second)))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
			// Loop to re-check.
		}
	}
}

// Record deducts the actual token usage from the budget.
// Call this after receiving the API response with real usage data.
func (tl *TPMRateLimiter) Record(tokens int) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.refillLocked(tl.nowFunc())
	tl.tokens -= float64(tokens)
	// Allow going negative — the next Wait/TryConsume will block until refilled.
}

// RateLimiterRegistry holds per-candidate rate limiters (both RPM and TPM).
// Candidates with RPM=0 / TPM=0 are unrestricted for that dimension.
// Thread-safe for concurrent reads/writes.
type RateLimiterRegistry struct {
	mu          sync.RWMutex
	limiters    map[string]*RateLimiter
	tpmLimiters map[string]*TPMRateLimiter
}

// NewRateLimiterRegistry creates an empty registry.
func NewRateLimiterRegistry() *RateLimiterRegistry {
	return &RateLimiterRegistry{
		limiters:    make(map[string]*RateLimiter),
		tpmLimiters: make(map[string]*TPMRateLimiter),
	}
}

// Register adds a rate limiter for the given key at the given RPM.
// If rpm <= 0, no limiter is registered (unrestricted).
func (r *RateLimiterRegistry) Register(key string, rpm int) {
	if rpm <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limiters[key] = newRateLimiter(rpm)
}

// RegisterTPM adds a TPM rate limiter for the given key.
// If tpm <= 0, no limiter is registered (unrestricted).
func (r *RateLimiterRegistry) RegisterTPM(key string, tpm int) {
	if tpm <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tpmLimiters[key] = newTPMRateLimiter(tpm)
}

// Wait acquires a token for the given key, blocking if needed.
// If no limiter is registered for key, returns immediately.
func (r *RateLimiterRegistry) Wait(ctx context.Context, key string) error {
	r.mu.RLock()
	rl := r.limiters[key]
	r.mu.RUnlock()
	if rl == nil {
		return nil
	}
	return rl.Wait(ctx)
}

// TryAcquire attempts to consume a token for the given key without blocking.
// If no limiter is registered for key, it returns true.
func (r *RateLimiterRegistry) TryAcquire(key string) bool {
	r.mu.RLock()
	rl := r.limiters[key]
	r.mu.RUnlock()
	if rl == nil {
		return true
	}
	return rl.TryAcquire()
}

// TPMTryConsume checks if the TPM budget can cover the estimated tokens.
// Returns true if no TPM limiter is registered or if budget is available.
func (r *RateLimiterRegistry) TPMTryConsume(key string, estimate int) bool {
	r.mu.RLock()
	tl := r.tpmLimiters[key]
	r.mu.RUnlock()
	if tl == nil {
		return true
	}
	return tl.TryConsume(estimate)
}

// TPMWait blocks until the TPM budget can cover the estimated tokens.
func (r *RateLimiterRegistry) TPMWait(ctx context.Context, key string, estimate int) error {
	r.mu.RLock()
	tl := r.tpmLimiters[key]
	r.mu.RUnlock()
	if tl == nil {
		return nil
	}
	return tl.Wait(ctx, estimate)
}

// TPMRecord deducts actual token usage from the TPM budget.
func (r *RateLimiterRegistry) TPMRecord(key string, tokens int) {
	r.mu.RLock()
	tl := r.tpmLimiters[key]
	r.mu.RUnlock()
	if tl == nil {
		return
	}
	tl.Record(tokens)
}

// RegisterCandidates registers rate limiters for all candidates that have RPM > 0 or TPM > 0.
func (r *RateLimiterRegistry) RegisterCandidates(candidates []FallbackCandidate) {
	for _, c := range candidates {
		if c.RPM > 0 {
			r.Register(c.StableKey(), c.RPM)
		}
		if c.TPM > 0 {
			r.RegisterTPM(c.StableKey(), c.TPM)
		}
	}
}
