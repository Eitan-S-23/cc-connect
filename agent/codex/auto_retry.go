package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Progress is reported independently of rendering: assistant messages may be
// buffered until turn completion, and intermediate ones are rendered as thinking.
type retryProgress struct {
	retrySendMu sync.Mutex
	retryMu     sync.Mutex
	onProgress  func(string)
}

func (p *retryProgress) SetRetryProgressHandler(handler func(string)) {
	p.retryMu.Lock()
	defer p.retryMu.Unlock()
	p.onProgress = handler
}

func (p *retryProgress) reportRetryProgress(text string) {
	p.retryMu.Lock()
	defer p.retryMu.Unlock()
	if p.onProgress != nil {
		p.onProgress(text)
	}
}

var retryAfterPattern = regexp.MustCompile(`(?i)(?:retry[-_ ]?after|try again in)["\s:=]+([0-9]+(?:\.[0-9]+)?)\s*(ms|milliseconds?|s|seconds?|m|minutes?)?`)
var retryAfterDatePattern = regexp.MustCompile(`(?i)retry[-_ ]?after["\s:=]+([a-z]{3},\s*[0-9]{1,2}\s+[a-z]{3}\s+[0-9]{4}\s+[0-9]{2}:[0-9]{2}:[0-9]{2}\s+GMT)`)

func codexRateLimitRetryDelay(err error) (time.Duration, bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	text := strings.ToLower(err.Error())
	for _, permanent := range []string{"insufficient_quota", "quota_exceeded", "quota exhausted", "exceeded your current quota", "billing", "insufficient balance", "insufficient credit", "invalid_api_key", "invalid api key", "unauthorized", "unauthenticated", "model_not_found", "model not found", "unrecognized_model", "invalid_encrypted_content"} {
		if strings.Contains(text, permanent) {
			return 0, false
		}
	}
	if !strings.Contains(text, "rate limit") && !strings.Contains(text, "rate_limit_exceeded") && !strings.Contains(text, "rate_limit_error") {
		return 0, false
	}
	var serverDelay interface{ RetryAfter() time.Duration }
	if errors.As(err, &serverDelay) {
		return max(0, serverDelay.RetryAfter()), true
	}
	if match := retryAfterDatePattern.FindStringSubmatch(err.Error()); len(match) > 0 {
		if reset, parseErr := http.ParseTime(match[1]); parseErr == nil {
			return max(0, time.Until(reset)), true
		}
	}
	if match := retryAfterPattern.FindStringSubmatch(text); len(match) > 0 {
		value, _ := strconv.ParseFloat(match[1], 64)
		unit := time.Second
		if strings.HasPrefix(match[2], "ms") || strings.HasPrefix(match[2], "millisecond") {
			unit = time.Millisecond
		} else if strings.HasPrefix(match[2], "m") {
			unit = time.Minute
		}
		if value > float64((1<<63-1)/int64(unit)) {
			return 0, false // Do not retry earlier than an unrepresentable minimum.
		}
		return time.Duration(value * float64(unit)), true
	}
	return 0, true
}

func (cs *codexSession) RateLimitRetryDelay(err error) (time.Duration, bool) {
	return codexRateLimitRetryDelay(err)
}

func (s *appServerSession) RateLimitRetryDelay(err error) (time.Duration, bool) {
	return codexRateLimitRetryDelay(err)
}

func waitRetryReady(ctx context.Context, ready func() bool) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ready() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *retryProgress) waitSend(ctx context.Context) error {
	return waitRetryReady(ctx, func() bool {
		if !p.retrySendMu.TryLock() {
			return false
		}
		p.retrySendMu.Unlock()
		return true
	})
}

func (cs *codexSession) PrepareRetry(ctx context.Context) error {
	if err := cs.waitSend(ctx); err != nil {
		return err
	}
	return waitRetryReady(ctx, func() bool {
		cs.cmdMu.Lock()
		defer cs.cmdMu.Unlock()
		return len(cs.cmds) == 0
	})
}

func (s *appServerSession) PrepareRetry(ctx context.Context) error {
	if err := s.waitSend(ctx); err != nil {
		return err
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.currentTurn != "" {
		return fmt.Errorf("previous turn is still active")
	}
	return nil
}

// The read loop can finish a fast failed turn before Send processes the RPC
// reply. That delayed reply must not resurrect the already finished turn.
func (s *appServerSession) recordStartedTurn(id string) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if id == "" || id == s.lastFinishedTurn || id == s.currentTurn {
		return
	}
	s.currentTurn = id
	s.pendingMsgs = s.pendingMsgs[:0]
}

var _ core.RateLimitRetrySession = (*codexSession)(nil)
var _ core.RateLimitRetrySession = (*appServerSession)(nil)
