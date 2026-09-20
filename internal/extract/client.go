package extract

// client.go is the house outbound HTTP client (010 contract §1): the one
// client every outward fetch goes through once cmd/lw wires it in (T-D).
// It carries lw's User-Agent on every request, refuses before dial to
// fetch a literal-IP host no outbound fetch should ever talk to
// (link-local unicast/multicast or unspecified — the shape of a cloud
// metadata endpoint or an unspecified target), and follows redirects
// under a policy that re-validates every hop's host.
//
// The 2 MiB body cap is MaxBodyBytes here but enforced in html.go's read
// (io.LimitReader), where the response body is actually consumed; local
// file reads stay unbounded.
//
// Loopback and RFC1918 stay ALLOWED (C-1006): the entire test pattern —
// this package's own suite, internal/tools' ingest tests, and every
// future fixture — runs on httptest loopback servers, so a guard that
// blocked 127.0.0.1 would break every test in the tree. The residual
// loopback/RFC1918 exposure is accepted and logged in plan §6.

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// UserAgent is the User-Agent every outbound house request carries —
// servers (and the operators reading their logs) should be able to tell
// lw from a browser or a scraper.
const UserAgent = "lw (+https://github.com/awepo-pro/lw)"

// MaxBodyBytes caps one fetched page at 2 MiB: more than any real article
// needs, small enough that a hostile or misconfigured origin cannot balloon
// the extractor's memory or, downstream, the vault. html.read reads through
// an io.LimitReader(MaxBodyBytes+1) so an oversized body fails fast with
// errBodyExceedsLimit instead of being read whole and rejected afterwards.
const MaxBodyBytes = 2 << 20

// errBodyExceedsLimit is what html.read returns when a fetched body runs
// past MaxBodyBytes. It is a sentinel so callers can errors.Is it; its
// text is the contract's exact message.
var errBodyExceedsLimit = errors.New("extract: body exceeds 2 MiB limit")

// maxRedirects is the redirect budget NewHTTPClient keeps. It restates
// net/http's default because installing a CheckRedirect replaces the
// default policy — cap included — wholesale, and a house policy must not
// silently widen the budget as a side effect.
const maxRedirects = 10

// errTooManyRedirects mirrors net/http's own default-policy text; the
// *url.Error wrapper supplies the surrounding "Get …:" wording, as it
// does for the built-in policy.
var errTooManyRedirects = errors.New("stopped after 10 redirects")

// NewHTTPClient returns the house client for outbound fetches: UserAgent
// on every request, a 2 MiB body cap enforced in html.read, and a
// redirect policy that validates every hop's host. cmd/lw wires it into
// NewHTML (T-D); until then NewHTML keeps working with whatever client it
// is handed, unguarded — the guard ships with this constructor.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		// guardedTransport stamps UserAgent and re-runs the host guard on
		// every dial — including the initial request, which CheckRedirect
		// never sees, so the two overlap only on redirect hops.
		Transport: &guardedTransport{base: http.DefaultTransport},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if err := guardURL(req.URL); err != nil {
				return err
			}
			if len(via) >= maxRedirects {
				return errTooManyRedirects
			}
			return nil
		},
	}
}

// guardedTransport wraps http.DefaultTransport with the two per-request
// house rules: the host guard runs before anything is dialed, and the
// request goes out stamped with UserAgent.
type guardedTransport struct {
	base http.RoundTripper
}

// RoundTrip guards req's host, stamps UserAgent, and delegates to the
// base transport. RoundTrip must not modify the request it is given
// (net/http's contract), so the header is set on a clone.
func (t *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := guardURL(req.URL); err != nil {
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set("User-Agent", UserAgent)
	return t.base.RoundTrip(r)
}

// guardURL rejects the literal-IP hosts the house client must never dial:
// link-local unicast (169.254/16, fe80::/10 — cloud metadata endpoints
// live here), link-local multicast, and the unspecified address. The
// literal check goes through netip.ParseAddr, not net.ParseIP: only the
// former still classifies zone-qualified IPv6 literals (RFC 6874 —
// Hostname() yields e.g. "fe80::1%eth0") and IPv4-mapped forms, so a
// link-local literal cannot pose as a name by carrying a zone. It is
// deliberately a literal-IP check only (010 contract §1): a name is never
// resolved here, so the guard itself makes no network call and adds no
// DNS dependency to the fetch path.
func guardURL(u *url.URL) error {
	host := u.Hostname()
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil // not a literal IP; names pass (contract: literal-IP check is sufficient)
	}
	if addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsUnspecified() {
		return &blockedHostError{host: host}
	}
	return nil
}

// blockedHostError is the guard's verdict: host is a literal IP in a range
// the house client must not dial. net/http wraps whatever CheckRedirect or
// the transport returns in a *url.Error; html.read unwraps this type back
// out (errors.As) so callers see the bare, exact message.
type blockedHostError struct {
	host string
}

func (e *blockedHostError) Error() string {
	return fmt.Sprintf("extract: blocked non-global host %s", e.host)
}
