package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHTTPClientGuard is the permanent regression suite for the house
// outbound client (010 contract §1; D-10C: it ships here and is never
// deleted or loosened). It pins the three hardenings at once:
//
//   - UserAgent on the wire, exactly;
//   - a 2 MiB body cap in the real read path, with the contract's exact
//     error;
//   - a per-hop host guard that blocks link-local and unspecified
//     literal-IP hosts before anything is dialed, with the contract's
//     exact error prefix, while leaving loopback — the home of every
//     httptest-based test in this tree — alone (C-1006).
//
// Every subtest drives the real NewHTML chain (Chain over NewHTML with
// the NewHTTPClient client, the shape cmd/lw builds) against loopback
// httptest servers, never the client or the guard in isolation (C-815).
func TestHTTPClientGuard(t *testing.T) {
	const page = `<html><body><h1>Guard Harness</h1><p>reachable</p></body></html>`
	serve := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}
	ex := Chain(NewHTML(NewHTTPClient(10*time.Second)), NewFile())
	ctx := context.Background()

	t.Run("allows_loopback", func(t *testing.T) {
		// The guard must never break the test pattern: an ordinary
		// httptest loopback URL extracts exactly as before C-1006.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serve(w, page)
		}))
		defer srv.Close()

		doc, err := ex.Extract(ctx, srv.URL)
		if err != nil {
			t.Fatalf("Extract(%s) error: %v", srv.URL, err)
		}
		if doc.Title != "Guard Harness" {
			t.Errorf("Title = %q, want %q", doc.Title, "Guard Harness")
		}
	})

	t.Run("blocks_non_global_redirect_host", func(t *testing.T) {
		// A redirect hop pointing at the link-local literal 169.254.169.254
		// (the cloud metadata endpoint) must be refused with the guard's
		// exact prefix — nothing may be dialed there.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}))
		defer srv.Close()

		_, err := ex.Extract(ctx, srv.URL)
		if err == nil {
			t.Fatal("Extract() error = nil, want the redirect to a link-local host blocked")
		}
		if !strings.HasPrefix(err.Error(), "extract: blocked non-global host ") {
			t.Fatalf("Extract() error = %q, want prefix %q", err.Error(), "extract: blocked non-global host ")
		}
	})

	t.Run("blocks_non_global_target_host", func(t *testing.T) {
		// Fetching a literal link-local URL is rejected before dial — the
		// guard fires without any connection being attempted.
		_, err := ex.Extract(ctx, "http://169.254.169.254/latest/meta-data/")
		if err == nil {
			t.Fatal("Extract() error = nil, want a link-local target host blocked")
		}
		if !strings.HasPrefix(err.Error(), "extract: blocked non-global host ") {
			t.Fatalf("Extract() error = %q, want prefix %q", err.Error(), "extract: blocked non-global host ")
		}
	})

	t.Run("blocks_non_global_zone_literal", func(t *testing.T) {
		// A zone-qualified IPv6 literal (RFC 6874: %25 is the URL-escaped
		// '%') is a link-local address, not a name — a literal parser that
		// returns nil on the zone form lets it reach the dialer. The guard
		// must refuse it before dial, zone included in the error, so no
		// server sits behind the URL and none is needed.
		_, err := ex.Extract(ctx, "http://[fe80::1%25eth0]/x")
		if err == nil {
			t.Fatal("Extract() error = nil, want a zone-qualified link-local literal blocked")
		}
		if !strings.HasPrefix(err.Error(), "extract: blocked non-global host fe80::1%eth0") {
			t.Fatalf("Extract() error = %q, want prefix %q", err.Error(), "extract: blocked non-global host fe80::1%eth0")
		}
	})

	t.Run("caps_body_size", func(t *testing.T) {
		// A 3 MiB body overshoots MaxBodyBytes; Extract must fail with the
		// contract's exact error, not wrap it or truncate silently.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serve(w, strings.Repeat("x", 3<<20))
		}))
		defer srv.Close()

		_, err := ex.Extract(ctx, srv.URL)
		if err == nil {
			t.Fatal("Extract() error = nil, want the 2 MiB body cap enforced")
		}
		if err.Error() != "extract: body exceeds 2 MiB limit" {
			t.Fatalf("Extract() error = %q, want exactly %q", err.Error(), "extract: body exceeds 2 MiB limit")
		}
	})

	t.Run("follows_redirect_within_policy", func(t *testing.T) {
		// A 302 to another loopback path is within policy: followed, and
		// the destination page extracted normally.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ok" {
				serve(w, `<html><body><h1>Redirected OK</h1></body></html>`)
				return
			}
			http.Redirect(w, r, "/ok", http.StatusFound)
		}))
		defer srv.Close()

		doc, err := ex.Extract(ctx, srv.URL)
		if err != nil {
			t.Fatalf("Extract(%s) error: %v", srv.URL, err)
		}
		if doc.Title != "Redirected OK" {
			t.Errorf("Title = %q, want %q", doc.Title, "Redirected OK")
		}
	})

	t.Run("sets_user_agent", func(t *testing.T) {
		// The wire carries UserAgent byte-for-byte. The handler hands the
		// header over a channel so the -race run sees a happens-before
		// edge, not a shared variable.
		uaCh := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uaCh <- r.Header.Get("User-Agent")
			serve(w, page)
		}))
		defer srv.Close()

		if _, err := ex.Extract(ctx, srv.URL); err != nil {
			t.Fatalf("Extract(%s) error: %v", srv.URL, err)
		}
		if got := <-uaCh; got != UserAgent {
			t.Errorf("User-Agent = %q, want exactly %q", got, UserAgent)
		}
	})
}
