package agent

// providerretry.go holds 061's recovery policy for a provider that REFUSED a
// round — an HTTP error status from Stream, or an error event inside a 200
// stream (both arrive as a *llm.ProviderError). It sits beside streamcut.go's
// 035 policy and does not touch it: a cut stream is not a provider error, and
// the two budgets are separate.
//
// Why it exists: s47's first OpenRouter ingest lost two staged pages and 157 k
// input tokens to one transient upstream error in round 7, and 035's single
// immediate re-send cannot outlast a rate limit — the second attempt lands in
// the same second as the first. A refusal the provider marks transient
// (llm.ProviderError.Retryable) is therefore waited out and the identical
// request sent again, up to maxProviderRetries times per round; everything
// else fails the turn with the provider's own error intact in the chain.
//
// As in 035, whether the round already dispatched a tool call decides
// everything: a call that ran must not run twice, so a refusal after one ends
// the round as finished (loop.go) and never reaches this file's delay rule.

import (
	"context"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/awepo-pro/lw/internal/llm"
)

// maxProviderRetries is a round's retry budget for provider refusals (061).
// Per round, never per turn, and separate from 035's one cut retry.
const maxProviderRetries = 3

// maxRetryAfterWait is the longest Retry-After the loop waits out. Past a
// minute the provider is naming a quota reset, not a pause, and a CLI that
// sits silent that long is indistinguishable from a hang — the turn fails now
// and the user decides.
const maxRetryAfterWait = 60 * time.Second

// providerBackoff is the wait before retry 1, 2 and 3 when the provider names
// none: doubling from two seconds, so three retries span 14 s — long enough
// for a burst limit to clear, short enough that a dead provider fails fast.
var providerBackoff = [maxProviderRetries]time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}

// providerRetryReasonRunes bounds the provider's message inside a RetryEv
// reason; the reason is one line on stdout and in the trace.
const providerRetryReasonRunes = 120

// providerRetryDelay decides whether refusal pe is retried when retries have
// already been made this round, and how long to wait first. A refusal the
// provider does not mark retryable, a spent budget and a Retry-After past
// maxRetryAfterWait are all "no"; otherwise the wait is the provider's own
// Retry-After when it names one, else the next backoff step.
func providerRetryDelay(pe *llm.ProviderError, retries int) (time.Duration, bool) {
	if !pe.Retryable() || retries >= maxProviderRetries {
		return 0, false
	}
	if pe.RetryAfter > maxRetryAfterWait {
		return 0, false
	}
	if pe.RetryAfter > 0 {
		return pe.RetryAfter, true
	}
	return providerBackoff[retries], true
}

// providerRetryReason is the RetryEv reason and trace line for a retry:
// `provider <code>: <message>; retrying in <N>s` (llm.ProviderError.Short is
// the first half; Message is the provider's own sentence, unwrapped from an
// HTTP error body by the client — 061 FIX1). The message is cut to its
// first providerRetryReasonRunes runes (never mid-rune) and N is the wait
// rounded UP to whole seconds, so a sub-second Retry-After never reads "0s".
func providerRetryReason(pe *llm.ProviderError, delay time.Duration) string {
	msg := pe.Message
	if utf8.RuneCountInString(msg) > providerRetryReasonRunes {
		msg = string([]rune(msg)[:providerRetryReasonRunes])
	}
	secs := int((delay + time.Second - 1) / time.Second)
	return pe.Short() + ": " + msg + "; retrying in " + strconv.Itoa(secs) + "s"
}

// sleepCtx waits d, or until ctx ends — whichever is first — and reports
// ctx's error when it was the context. It is Loop.sleep's default.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// wait waits out a provider refusal through the Loop's injected sleep, or
// sleepCtx when none is set.
func (l *Loop) wait(ctx context.Context, d time.Duration) error {
	if l.sleep != nil {
		return l.sleep(ctx, d)
	}
	return sleepCtx(ctx, d)
}
