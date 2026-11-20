package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// keepSession renews s until stop is called. If renewal fails, it cancels the publish.
func (c *client) keepSession(parent context.Context, s *session) (context.Context, func(), error) {
	renew := func(ctx context.Context) (time.Time, time.Duration, error) {
		var out struct {
			ValidForMS   int64 `json:"valid_for_ms"`
			RenewAfterMS int64 `json:"renew_after_ms"`
		}
		start := time.Now()
		_, err := c.callOnce(ctx, request{
			method: http.MethodPost, path: "/api/publish/sessions/" + strconv.FormatInt(s.ID, 10) + "/renew",
			token: s.Token, json: map[string]string{"renewal_key": s.RenewalKey}, out: &out, timeout: 10 * time.Second,
		})
		if err != nil {
			return time.Time{}, 0, err
		}
		deadline := start.Add(time.Duration(out.ValidForMS) * time.Millisecond)
		delay := time.Duration(out.RenewAfterMS) * time.Millisecond
		if delay <= 0 || time.Until(deadline) <= 0 {
			return time.Time{}, 0, fmt.Errorf("the server returned an expired renewal")
		}
		return deadline, delay, nil
	}
	deadline, delay, err := renew(parent)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot start publish session renewal: %w", err)
	}
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			timer := time.NewTimer(max(0, min(delay, time.Until(deadline))))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			attempt, finish := context.WithDeadline(ctx, deadline)
			next, after, err := renew(attempt)
			finish()
			if err == nil {
				deadline, delay = next, after
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if refused(err) || time.Until(deadline) <= 0 {
				cancel(fmt.Errorf("publish session ended: %w", err))
				return
			}
			delay = min(delay, 2*time.Second)
		}
	}()
	return ctx, func() {
		cancel(nil)
		<-done
	}, nil
}
