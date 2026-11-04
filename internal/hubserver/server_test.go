package hubserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

type fakeService struct {
	helloFn    func(ctx context.Context) (hub.Hello, error)
	manifestFn func(ctx context.Context) (hub.Manifest, error)
	readFn     func(ctx context.Context, path string) ([]byte, string, error)
	putFn      func(ctx context.Context, dev hub.Device, path, base string, body []byte) (hub.PutResult, error)
	deleteFn   func(ctx context.Context, dev hub.Device, path, base string) (hub.DeleteResult, error)
	changesFn  func(ctx context.Context, since string) (hub.Changes, error)
}

func (f *fakeService) Hello(ctx context.Context) (hub.Hello, error) {
	if f.helloFn != nil {
		return f.helloFn(ctx)
	}
	return hub.Hello{Protocol: hub.ProtocolVersion, LWVersion: "0.7.9", VaultFormat: 1, Head: "abc1234"}, nil
}

func (f *fakeService) Manifest(ctx context.Context) (hub.Manifest, error) {
	if f.manifestFn != nil {
		return f.manifestFn(ctx)
	}
	return hub.Manifest{Head: "abc1234", Files: []hub.FileEntry{}}, nil
}

func (f *fakeService) Read(ctx context.Context, path string) ([]byte, string, error) {
	if f.readFn != nil {
		return f.readFn(ctx, path)
	}
	return []byte("data"), "sha256hex", nil
}

func (f *fakeService) Put(ctx context.Context, dev hub.Device, path, base string, body []byte) (hub.PutResult, error) {
	if f.putFn != nil {
		return f.putFn(ctx, dev, path, base, body)
	}
	return hub.PutResult{Result: hub.ResultApplied, Path: path, SHA256: "sha256hex", Head: "abc1234"}, nil
}

func (f *fakeService) Delete(ctx context.Context, dev hub.Device, path, base string) (hub.DeleteResult, error) {
	if f.deleteFn != nil {
		return f.deleteFn(ctx, dev, path, base)
	}
	return hub.DeleteResult{Result: hub.ResultDeleted, Head: "abc1234"}, nil
}

func (f *fakeService) Changes(ctx context.Context, since string) (hub.Changes, error) {
	if f.changesFn != nil {
		return f.changesFn(ctx, since)
	}
	return hub.Changes{Head: "abc1234", Full: false, Changed: []hub.Change{}}, nil
}

type testEnv struct {
	handler http.Handler
	devices *Devices
	devPath string
	fake    *fakeService
	logs    []string
	now     time.Time
}

func setupTestEnv(t *testing.T, customNow func() time.Time) *testEnv {
	t.Helper()
	dir := t.TempDir()
	devPath := filepath.Join(dir, "devices.json")

	env := &testEnv{
		devPath: devPath,
		fake:    &fakeService{},
		now:     time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
	}

	clock := customNow
	if clock == nil {
		clock = func() time.Time { return env.now }
	}

	cfg := Config{
		Users:       []string{"awepo-pro@github"},
		DevicesPath: devPath,
		Now:         clock,
		Log: func(format string, args ...any) {
			env.logs = append(env.logs, fmt.Sprintf(format, args...))
		},
	}

	h, err := New(env.fake, cfg)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	env.handler = h

	d, err := OpenDevices(devPath)
	if err != nil {
		t.Fatalf("OpenDevices failed: %v", err)
	}
	env.devices = d

	return env
}

func (e *testEnv) approveDevice(t *testing.T, id, name string) {
	t.Helper()
	// Trigger pending registration first via an HTTP request
	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", id)
	req.Header.Set("X-LW-Device-Name", name)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected pending 403, got %d", w.Code)
	}
	// Now approve by ID
	if _, err := e.devices.Approve(id); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
}

// W1 GET /v1/hello from 127.0.0.1 with user awepo-pro@github (in Users) and an approved device -> 200, JSON Hello with device:"approved".
func TestW1Hello(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "Pixel 8 Pro!")

	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "dev-0001-aaaa")

	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("expected application/json, got %q", ct)
	}

	var hello hub.Hello
	if err := json.Unmarshal(w.Body.Bytes(), &hello); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if hello.Device != "approved" {
		t.Fatalf("expected hello.Device == approved, got %q", hello.Device)
	}
	if hello.Protocol != hub.ProtocolVersion {
		t.Fatalf("expected protocol %d, got %d", hub.ProtocolVersion, hello.Protocol)
	}
}

// W2 remote 100.70.80.96:5555 (non-loopback) -> 401 unauthorized; missing user header -> 401; user not in Users -> 401.
func TestW2RemoteAddrAndUserAuth(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "phone")

	// 1. Non-loopback remote addr -> 401 unauthorized
	{
		req := httptest.NewRequest("GET", "/v1/hello", nil)
		req.RemoteAddr = "100.70.80.96:5555"
		req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
		req.Header.Set("X-LW-Device", "dev-0001-aaaa")
		w := httptest.NewRecorder()
		env.handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for non-loopback, got %d: %s", w.Code, w.Body.String())
		}
		var errBody hub.ErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeUnauthorized {
			t.Fatalf("expected code %s, got %v", hub.CodeUnauthorized, errBody)
		}
	}

	// 2. Missing user header -> 401 unauthorized
	{
		req := httptest.NewRequest("GET", "/v1/hello", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("X-LW-Device", "dev-0001-aaaa")
		w := httptest.NewRecorder()
		env.handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for missing user header, got %d: %s", w.Code, w.Body.String())
		}
		var errBody hub.ErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeUnauthorized {
			t.Fatalf("expected code %s, got %v", hub.CodeUnauthorized, errBody)
		}
	}

	// 3. User not in Users -> 401 unauthorized
	{
		req := httptest.NewRequest("GET", "/v1/hello", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("Tailscale-User-Login", "stranger@github")
		req.Header.Set("X-LW-Device", "dev-0001-aaaa")
		w := httptest.NewRecorder()
		env.handler.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for unknown user, got %d: %s", w.Code, w.Body.String())
		}
		var errBody hub.ErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeUnauthorized {
			t.Fatalf("expected code %s, got %v", hub.CodeUnauthorized, errBody)
		}
	}
}

// W3 missing/invalid X-LW-Device ('abc', 65 chars, 'a b c d e f g h') -> 400 bad_request.
func TestW3DeviceHeaderValidation(t *testing.T) {
	env := setupTestEnv(t, nil)

	cases := []struct {
		name     string
		deviceID string
		missing  bool
	}{
		{"missing", "", true},
		{"too short (3 chars)", "abc", false},
		{"too long (65 chars)", strings.Repeat("a", 65), false},
		{"spaces", "a b c d e f g h", false},
		{"special char", "dev-0001@aaaa", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v1/hello", nil)
			req.RemoteAddr = "127.0.0.1:40000"
			req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
			if !tc.missing {
				req.Header.Set("X-LW-Device", tc.deviceID)
			}
			w := httptest.NewRecorder()
			env.handler.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for device ID %q, got %d: %s", tc.deviceID, w.Code, w.Body.String())
			}
			var errBody hub.ErrorBody
			if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeBadRequest {
				t.Fatalf("expected code %s, got %v", hub.CodeBadRequest, errBody)
			}
		})
	}
}

// W4 first request from unknown device dev-0001-aaaa named Pixel 8 Pro! -> 403 device_pending;
// the store now lists it pending, slug pixel-8-pro, user recorded; a second device with the same name -> slug pixel-8-pro-2.
func TestW4UnknownDevicePending(t *testing.T) {
	env := setupTestEnv(t, nil)

	// First request from dev-0001-aaaa
	req1 := httptest.NewRequest("GET", "/v1/hello", nil)
	req1.RemoteAddr = "127.0.0.1:40000"
	req1.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req1.Header.Set("X-LW-Device", "dev-0001-aaaa")
	req1.Header.Set("X-LW-Device-Name", "Pixel 8 Pro!")
	req1.Header.Set("X-Forwarded-For", "100.70.80.96")
	w1 := httptest.NewRecorder()
	env.handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w1.Code, w1.Body.String())
	}
	var errBody hub.ErrorBody
	if err := json.Unmarshal(w1.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeDevicePending {
		t.Fatalf("expected code %s, got %v", hub.CodeDevicePending, errBody)
	}

	// Verify the store lists it pending, slug pixel-8-pro, user recorded
	list1, err := env.devices.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list1) != 1 {
		t.Fatalf("expected 1 device, got %d", len(list1))
	}
	rec1 := list1[0]
	if rec1.ID != "dev-0001-aaaa" || rec1.State != hub.DevicePending || rec1.Slug != "pixel-8-pro" || rec1.User != "awepo-pro@github" {
		t.Fatalf("unexpected record: %+v", rec1)
	}
	if rec1.LastAddr != "100.70.80.96" {
		t.Fatalf("expected LastAddr 100.70.80.96, got %q", rec1.LastAddr)
	}

	// Second request from unknown dev-0002-bbbb named Pixel 8 Pro!
	req2 := httptest.NewRequest("GET", "/v1/hello", nil)
	req2.RemoteAddr = "127.0.0.1:40000"
	req2.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req2.Header.Set("X-LW-Device", "dev-0002-bbbb")
	req2.Header.Set("X-LW-Device-Name", "Pixel 8 Pro!")
	w2 := httptest.NewRecorder()
	env.handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", w2.Code, w2.Body.String())
	}

	list2, err := env.devices.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list2) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(list2))
	}
	var rec2 *DeviceRecord
	for i := range list2 {
		if list2[i].ID == "dev-0002-bbbb" {
			rec2 = &list2[i]
			break
		}
	}
	if rec2 == nil {
		t.Fatalf("dev-0002-bbbb not found in store")
	}
	if rec2.Slug != "pixel-8-pro-2" {
		t.Fatalf("expected slug pixel-8-pro-2, got %q", rec2.Slug)
	}
}

// W5 Approve("pixel-8-pro") then request -> 200; Revoke -> 403 device_revoked; Approve("nope") -> error containing nope.
func TestW5ApproveAndRevoke(t *testing.T) {
	env := setupTestEnv(t, nil)

	// Register device as pending
	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "dev-0001-aaaa")
	req.Header.Set("X-LW-Device-Name", "Pixel 8 Pro!")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 pending, got %d", w.Code)
	}

	// 1. Approve("pixel-8-pro") then request -> 200
	approvedRec, err := env.devices.Approve("pixel-8-pro")
	if err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	if approvedRec.State != hub.DeviceApproved {
		t.Fatalf("expected approved state, got %s", approvedRec.State)
	}

	reqApproved := httptest.NewRequest("GET", "/v1/hello", nil)
	reqApproved.RemoteAddr = "127.0.0.1:40000"
	reqApproved.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqApproved.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wApproved := httptest.NewRecorder()
	env.handler.ServeHTTP(wApproved, reqApproved)
	if wApproved.Code != http.StatusOK {
		t.Fatalf("expected 200 after approve, got %d: %s", wApproved.Code, wApproved.Body.String())
	}

	// 2. Revoke -> 403 device_revoked
	revokedRec, err := env.devices.Revoke("pixel-8-pro")
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if revokedRec.State != hub.DeviceRevoked {
		t.Fatalf("expected revoked state, got %s", revokedRec.State)
	}

	reqRevoked := httptest.NewRequest("GET", "/v1/hello", nil)
	reqRevoked.RemoteAddr = "127.0.0.1:40000"
	reqRevoked.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqRevoked.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wRevoked := httptest.NewRecorder()
	env.handler.ServeHTTP(wRevoked, reqRevoked)
	if wRevoked.Code != http.StatusForbidden {
		t.Fatalf("expected 403 after revoke, got %d: %s", wRevoked.Code, wRevoked.Body.String())
	}
	var errBody hub.ErrorBody
	if err := json.Unmarshal(wRevoked.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeDeviceRevoked {
		t.Fatalf("expected code %s, got %v", hub.CodeDeviceRevoked, errBody)
	}

	// 3. Approve("nope") -> error containing nope
	_, errNope := env.devices.Approve("nope")
	if errNope == nil || !strings.Contains(errNope.Error(), "nope") {
		t.Fatalf("expected error containing 'nope', got %v", errNope)
	}
}

// W6 PUT /v1/file?path=notes/a.md&base=new -> the fake receives Device{ID, Name:"pixel-8-pro"}, path, base, body;
// body of MaxBodyBytes+1 -> 413 too_large and the fake is not called.
func TestW6PutFile(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "Pixel 8 Pro!")

	var receivedDev hub.Device
	var receivedPath, receivedBase string
	var receivedBody []byte
	called := false

	env.fake.putFn = func(ctx context.Context, dev hub.Device, path, base string, body []byte) (hub.PutResult, error) {
		called = true
		receivedDev = dev
		receivedPath = path
		receivedBase = base
		receivedBody = body
		return hub.PutResult{Result: hub.ResultApplied, Path: path, SHA256: "hexsha", Head: "commit1"}, nil
	}

	// 1. Valid PUT
	content := []byte("hello notes")
	req := httptest.NewRequest("PUT", "/v1/file?path=notes/a.md&base=new", bytes.NewReader(content))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "dev-0001-aaaa")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatalf("expected fake.putFn to be called")
	}
	if receivedDev.ID != "dev-0001-aaaa" || receivedDev.Name != "pixel-8-pro" {
		t.Fatalf("expected dev ID dev-0001-aaaa Name pixel-8-pro, got %+v", receivedDev)
	}
	if receivedPath != "notes/a.md" || receivedBase != "new" || !bytes.Equal(receivedBody, content) {
		t.Fatalf("unexpected call args: path=%s base=%s body=%s", receivedPath, receivedBase, string(receivedBody))
	}

	// 2. Body of MaxBodyBytes + 1 -> 413 too_large and fake not called
	called = false
	largeBody := make([]byte, hub.MaxBodyBytes+1)
	reqLarge := httptest.NewRequest("PUT", "/v1/file?path=notes/a.md&base=new", bytes.NewReader(largeBody))
	reqLarge.RemoteAddr = "127.0.0.1:40000"
	reqLarge.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqLarge.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wLarge := httptest.NewRecorder()
	env.handler.ServeHTTP(wLarge, reqLarge)

	if wLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", wLarge.Code, wLarge.Body.String())
	}
	if called {
		t.Fatalf("expected fake NOT to be called on oversized body")
	}
	var errBody hub.ErrorBody
	if err := json.Unmarshal(wLarge.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeTooLarge {
		t.Fatalf("expected code %s, got %v", hub.CodeTooLarge, errBody)
	}
}

// W7 GET /v1/file -> bytes + X-LW-SHA256 + ETag: "<hex>"; the fake's *hub.Error{not_found} -> 404 JSON.
func TestW7GetFile(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "phone")

	expectedBytes := []byte("some vault file contents")
	expectedSHA := "abcdef1234567890abcdef1234567890"

	env.fake.readFn = func(ctx context.Context, path string) ([]byte, string, error) {
		if path == "notes/a.md" {
			return expectedBytes, expectedSHA, nil
		}
		return nil, "", &hub.Error{Code: hub.CodeNotFound, Message: "file not found"}
	}

	// 1. Success case
	{
		req := httptest.NewRequest("GET", "/v1/file?path=notes/a.md", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
		req.Header.Set("X-LW-Device", "dev-0001-aaaa")
		w := httptest.NewRecorder()
		env.handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
			t.Fatalf("expected Content-Type application/octet-stream, got %q", ct)
		}
		if sha := w.Header().Get("X-LW-SHA256"); sha != expectedSHA {
			t.Fatalf("expected X-LW-SHA256 %s, got %q", expectedSHA, sha)
		}
		expectedETag := `"` + expectedSHA + `"`
		if etag := w.Header().Get("ETag"); etag != expectedETag {
			t.Fatalf("expected ETag %s, got %q", expectedETag, etag)
		}
		if !bytes.Equal(w.Body.Bytes(), expectedBytes) {
			t.Fatalf("body bytes mismatch")
		}
	}

	// 2. Not found error from service
	{
		req := httptest.NewRequest("GET", "/v1/file?path=notes/missing.md", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
		req.Header.Set("X-LW-Device", "dev-0001-aaaa")
		w := httptest.NewRecorder()
		env.handler.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
		}
		var errBody hub.ErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeNotFound {
			t.Fatalf("expected code %s, got %v", hub.CodeNotFound, errBody)
		}
	}
}

// W8 every code in section 2 maps to its status (table test over a fake returning each *hub.Error);
// a plain error -> 500 internal whose message does not leak the error text verbatim.
func TestW8ErrorMapping(t *testing.T) {
	codeStatusTable := []struct {
		code   string
		status int
	}{
		{hub.CodeBadRequest, http.StatusBadRequest},          // 400
		{hub.CodeUnauthorized, http.StatusUnauthorized},      // 401
		{hub.CodeOutOfScope, http.StatusForbidden},           // 403
		{hub.CodeDevicePending, http.StatusForbidden},        // 403
		{hub.CodeDeviceRevoked, http.StatusForbidden},        // 403
		{hub.CodeNotFound, http.StatusNotFound},              // 404
		{hub.CodeCaseCollision, http.StatusConflict},         // 409
		{hub.CodeTooLarge, http.StatusRequestEntityTooLarge}, // 413
		{hub.CodeInternal, http.StatusInternalServerError},   // 500
		{hub.CodeDiverged, http.StatusServiceUnavailable},    // 503
	}

	for _, tc := range codeStatusTable {
		t.Run(tc.code, func(t *testing.T) {
			env := setupTestEnv(t, nil)
			env.approveDevice(t, "dev-0001-aaaa", "phone")

			env.fake.manifestFn = func(ctx context.Context) (hub.Manifest, error) {
				return hub.Manifest{}, &hub.Error{Code: tc.code, Message: "test error message"}
			}

			req := httptest.NewRequest("GET", "/v1/manifest", nil)
			req.RemoteAddr = "127.0.0.1:40000"
			req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
			req.Header.Set("X-LW-Device", "dev-0001-aaaa")
			w := httptest.NewRecorder()
			env.handler.ServeHTTP(w, req)

			if w.Code != tc.status {
				t.Fatalf("code %s expected status %d, got %d: %s", tc.code, tc.status, w.Code, w.Body.String())
			}
			var errBody hub.ErrorBody
			if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
				t.Fatalf("invalid json: %v", err)
			}
			if errBody.Error.Code != tc.code {
				t.Fatalf("expected code %s, got %s", tc.code, errBody.Error.Code)
			}
			if errBody.Error.Message != "test error message" {
				t.Fatalf("expected message 'test error message', got %q", errBody.Error.Message)
			}
		})
	}

	// Plain error (non-*hub.Error) -> 500 internal whose message does not leak the error text verbatim
	t.Run("plain internal error", func(t *testing.T) {
		env := setupTestEnv(t, nil)
		env.approveDevice(t, "dev-0001-aaaa", "phone")

		secretPanic := "secret database credentials in disk /var/db/leaked.key"
		env.fake.manifestFn = func(ctx context.Context) (hub.Manifest, error) {
			return hub.Manifest{}, errors.New(secretPanic)
		}

		req := httptest.NewRequest("GET", "/v1/manifest", nil)
		req.RemoteAddr = "127.0.0.1:40000"
		req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
		req.Header.Set("X-LW-Device", "dev-0001-aaaa")
		w := httptest.NewRecorder()
		env.handler.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
		}
		var errBody hub.ErrorBody
		if err := json.Unmarshal(w.Body.Bytes(), &errBody); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		if errBody.Error.Code != hub.CodeInternal {
			t.Fatalf("expected code internal, got %s", errBody.Error.Code)
		}
		if strings.Contains(errBody.Error.Message, "secret") || strings.Contains(errBody.Error.Message, "disk") {
			t.Fatalf("error leaked secret details: %q", errBody.Error.Message)
		}
		if errBody.Error.Message != "internal error; see the hub log" {
			t.Fatalf("expected message 'internal error; see the hub log', got %q", errBody.Error.Message)
		}

		// Verify real error was passed to cfg.Log
		foundLog := false
		for _, l := range env.logs {
			if strings.Contains(l, secretPanic) {
				foundLog = true
				break
			}
		}
		if !foundLog {
			t.Fatalf("expected secret error to be logged in cfg.Log, got logs: %v", env.logs)
		}
	})
}

// W9 POST /v1/status -> 204, List() shows the report on that device; body > 64 KiB -> 413.
func TestW9StatusReport(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "Pixel 8 Pro!")

	// 1. Success case -> 204
	report := hub.StatusReport{
		PluginVersion: "0.7.9",
		LastError:     "something minor",
		QueueLen:      4,
		At:            "2026-11-03T12:00:00Z",
	}
	reportBytes, _ := json.Marshal(report)

	req := httptest.NewRequest("POST", "/v1/status", bytes.NewReader(reportBytes))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "dev-0001-aaaa")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	// Verify List() shows the report on that device
	list, err := env.devices.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 || list[0].Status == nil {
		t.Fatalf("expected 1 device with status, got: %+v", list)
	}
	if *list[0].Status != report {
		t.Fatalf("expected status report %+v, got %+v", report, *list[0].Status)
	}

	// 2. Body > 64 KiB -> 413
	oversized := make([]byte, hub.MaxStatusBytes+1)
	reqLarge := httptest.NewRequest("POST", "/v1/status", bytes.NewReader(oversized))
	reqLarge.RemoteAddr = "127.0.0.1:40000"
	reqLarge.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqLarge.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wLarge := httptest.NewRecorder()
	env.handler.ServeHTTP(wLarge, reqLarge)

	if wLarge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized status report, got %d: %s", wLarge.Code, wLarge.Body.String())
	}
	var errBody hub.ErrorBody
	if err := json.Unmarshal(wLarge.Body.Bytes(), &errBody); err != nil || errBody.Error.Code != hub.CodeTooLarge {
		t.Fatalf("expected code %s, got %v", hub.CodeTooLarge, errBody)
	}
}

// W10 unknown path -> 404 not_found; POST /v1/file -> 405 bad_request.
func TestW10Routing(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "phone")

	// 1. Unknown path -> 404 not_found
	reqUnknown := httptest.NewRequest("GET", "/v1/no-such-path", nil)
	reqUnknown.RemoteAddr = "127.0.0.1:40000"
	reqUnknown.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqUnknown.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wUnknown := httptest.NewRecorder()
	env.handler.ServeHTTP(wUnknown, reqUnknown)

	if wUnknown.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown path, got %d: %s", wUnknown.Code, wUnknown.Body.String())
	}
	var errUnknown hub.ErrorBody
	if err := json.Unmarshal(wUnknown.Body.Bytes(), &errUnknown); err != nil || errUnknown.Error.Code != hub.CodeNotFound {
		t.Fatalf("expected code %s, got %v", hub.CodeNotFound, errUnknown)
	}

	// 2. POST /v1/file -> 405 bad_request
	reqWrongMethod := httptest.NewRequest("POST", "/v1/file", strings.NewReader("body"))
	reqWrongMethod.RemoteAddr = "127.0.0.1:40000"
	reqWrongMethod.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqWrongMethod.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wWrongMethod := httptest.NewRecorder()
	env.handler.ServeHTTP(wWrongMethod, reqWrongMethod)

	if wWrongMethod.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for wrong method, got %d: %s", wWrongMethod.Code, wWrongMethod.Body.String())
	}
	var errWrongMethod hub.ErrorBody
	if err := json.Unmarshal(wWrongMethod.Body.Bytes(), &errWrongMethod); err != nil || errWrongMethod.Error.Code != hub.CodeBadRequest {
		t.Fatalf("expected code %s, got %v", hub.CodeBadRequest, errWrongMethod)
	}
}

// W11 devices file: mode 0600; concurrent Approve from 10 goroutines + requests leave a valid JSON file;
// LastSeen not rewritten twice within a minute (fake clock).
func TestW11DevicesStoreConcurrencyAndThrottling(t *testing.T) {
	// 1. File mode 0600 verification
	currTime := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	var timeMu sync.Mutex
	fakeClock := func() time.Time {
		timeMu.Lock()
		defer timeMu.Unlock()
		return currTime
	}

	env := setupTestEnv(t, fakeClock)
	env.approveDevice(t, "dev-0001-aaaa", "phone")

	fi, err := os.Stat(env.devPath)
	if err != nil {
		t.Fatalf("os.Stat devices.json failed: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("expected file permissions 0600, got %04o", perm)
	}

	// 2. LastSeen not rewritten twice within a minute (fake clock)
	// Device dev-0001-aaaa was registered and approved at currTime (12:00:00).
	list, err := env.devices.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("unexpected list: %v", list)
	}
	firstLastSeen := list[0].LastSeen

	// Stat before second request
	fiBefore, err := os.Stat(env.devPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	mtimeBefore := fiBefore.ModTime()

	// Advance clock by 30 seconds (< 1 minute)
	timeMu.Lock()
	currTime = currTime.Add(30 * time.Second)
	timeMu.Unlock()

	req2 := httptest.NewRequest("GET", "/v1/hello", nil)
	req2.RemoteAddr = "127.0.0.1:40000"
	req2.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req2.Header.Set("X-LW-Device", "dev-0001-aaaa")
	w2 := httptest.NewRecorder()
	env.handler.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w2.Code, w2.Body.String())
	}

	fiAfter30s, err := os.Stat(env.devPath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if fiAfter30s.ModTime() != mtimeBefore {
		t.Fatalf("devices file was rewritten within 1 minute!")
	}
	listAfter30s, _ := env.devices.List()
	if listAfter30s[0].LastSeen != firstLastSeen {
		t.Fatalf("expected LastSeen unchanged, got %s vs %s", listAfter30s[0].LastSeen, firstLastSeen)
	}

	// Advance clock by another 31 seconds (total 61s > 1 minute)
	timeMu.Lock()
	currTime = currTime.Add(31 * time.Second)
	timeMu.Unlock()

	req3 := httptest.NewRequest("GET", "/v1/hello", nil)
	req3.RemoteAddr = "127.0.0.1:40000"
	req3.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req3.Header.Set("X-LW-Device", "dev-0001-aaaa")
	w3 := httptest.NewRecorder()
	env.handler.ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w3.Code, w3.Body.String())
	}

	listAfter61s, _ := env.devices.List()
	expectedLastSeen := currTime.UTC().Format(time.RFC3339)
	if listAfter61s[0].LastSeen != expectedLastSeen {
		t.Fatalf("expected LastSeen %s, got %s", expectedLastSeen, listAfter61s[0].LastSeen)
	}

	// 3. Concurrent Approve from 10 goroutines + requests leave a valid JSON file
	var wg sync.WaitGroup
	// Register 10 devices first
	for i := 0; i < 10; i++ {
		devID := fmt.Sprintf("dev-%04d-test", i)
		reqInit := httptest.NewRequest("GET", "/v1/hello", nil)
		reqInit.RemoteAddr = "127.0.0.1:40000"
		reqInit.Header.Set("Tailscale-User-Login", "awepo-pro@github")
		reqInit.Header.Set("X-LW-Device", devID)
		reqInit.Header.Set("X-LW-Device-Name", fmt.Sprintf("Phone %d", i))
		wInit := httptest.NewRecorder()
		env.handler.ServeHTTP(wInit, reqInit)
	}

	// Concurrently approve and make requests
	for i := 0; i < 10; i++ {
		wg.Add(2)
		devID := fmt.Sprintf("dev-%04d-test", i)
		slug := fmt.Sprintf("phone-%d", i)

		// Goroutine 1: Approve
		go func(targetSlug string) {
			defer wg.Done()
			_, _ = env.devices.Approve(targetSlug)
		}(slug)

		// Goroutine 2: Request
		go func(id string) {
			defer wg.Done()
			reqC := httptest.NewRequest("GET", "/v1/hello", nil)
			reqC.RemoteAddr = "127.0.0.1:40000"
			reqC.Header.Set("Tailscale-User-Login", "awepo-pro@github")
			reqC.Header.Set("X-LW-Device", id)
			wC := httptest.NewRecorder()
			env.handler.ServeHTTP(wC, reqC)
		}(devID)
	}

	wg.Wait()

	// Verify valid JSON file
	rawJSON, err := os.ReadFile(env.devPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var testParsed struct {
		Devices []DeviceRecord `json:"devices"`
	}
	if err := json.Unmarshal(rawJSON, &testParsed); err != nil {
		t.Fatalf("devices file corrupted, invalid JSON: %v\ncontent:\n%s", err, string(rawJSON))
	}
	if len(testParsed.Devices) < 11 {
		t.Fatalf("expected at least 11 devices, got %d", len(testParsed.Devices))
	}
}

func TestManifestAndChanges(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "phone")

	env.fake.manifestFn = func(ctx context.Context) (hub.Manifest, error) {
		return hub.Manifest{
			Head: "head123",
			Files: []hub.FileEntry{
				{Path: "notes/a.md", SHA256: "sha1", Size: 10},
			},
		}, nil
	}

	reqManifest := httptest.NewRequest("GET", "/v1/manifest", nil)
	reqManifest.RemoteAddr = "127.0.0.1:40000"
	reqManifest.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqManifest.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wManifest := httptest.NewRecorder()
	env.handler.ServeHTTP(wManifest, reqManifest)

	if wManifest.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wManifest.Code, wManifest.Body.String())
	}
	var manifest hub.Manifest
	if err := json.Unmarshal(wManifest.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("unmarshal manifest failed: %v", err)
	}
	if manifest.Head != "head123" || len(manifest.Files) != 1 || manifest.Files[0].Path != "notes/a.md" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	env.fake.changesFn = func(ctx context.Context, since string) (hub.Changes, error) {
		if since != "head123" {
			t.Fatalf("expected since=head123, got %q", since)
		}
		return hub.Changes{
			Head: "head456",
			Full: false,
			Changed: []hub.Change{
				{Path: "notes/b.md", SHA256: nil},
			},
		}, nil
	}

	reqChanges := httptest.NewRequest("GET", "/v1/changes?since=head123", nil)
	reqChanges.RemoteAddr = "127.0.0.1:40000"
	reqChanges.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqChanges.Header.Set("X-LW-Device", "dev-0001-aaaa")
	wChanges := httptest.NewRecorder()
	env.handler.ServeHTTP(wChanges, reqChanges)

	if wChanges.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", wChanges.Code, wChanges.Body.String())
	}
	var changes hub.Changes
	if err := json.Unmarshal(wChanges.Body.Bytes(), &changes); err != nil {
		t.Fatalf("unmarshal changes failed: %v", err)
	}
	if changes.Head != "head456" || len(changes.Changed) != 1 || changes.Changed[0].Path != "notes/b.md" {
		t.Fatalf("unexpected changes: %+v", changes)
	}
}

func TestDeleteFile(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-0001-aaaa", "Pixel 8 Pro!")

	var receivedDev hub.Device
	var receivedPath, receivedBase string
	called := false

	env.fake.deleteFn = func(ctx context.Context, dev hub.Device, path, base string) (hub.DeleteResult, error) {
		called = true
		receivedDev = dev
		receivedPath = path
		receivedBase = base
		return hub.DeleteResult{Result: hub.ResultDeleted, Head: "head789"}, nil
	}

	req := httptest.NewRequest("DELETE", "/v1/file?path=notes/a.md&base=commit1", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "dev-0001-aaaa")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !called {
		t.Fatalf("expected deleteFn to be called")
	}
	if receivedDev.ID != "dev-0001-aaaa" || receivedDev.Name != "pixel-8-pro" {
		t.Fatalf("unexpected dev: %+v", receivedDev)
	}
	if receivedPath != "notes/a.md" || receivedBase != "commit1" {
		t.Fatalf("unexpected path/base: %s, %s", receivedPath, receivedBase)
	}

	var res hub.DeleteResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal delete result failed: %v", err)
	}
	if res.Result != hub.ResultDeleted || res.Head != "head789" {
		t.Fatalf("unexpected delete result: %+v", res)
	}
}

func TestFix1UnknownDeviceStateUnauthorized(t *testing.T) {
	env := setupTestEnv(t, nil)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	_, err := env.devices.RegisterPending("device-test-fix1", "Test Fix 1", "awepo-pro@github", "127.0.0.1", now)
	if err != nil {
		t.Fatalf("RegisterPending failed: %v", err)
	}
	records, err := env.devices.readFile()
	if err != nil {
		t.Fatalf("readFile failed: %v", err)
	}
	for i := range records {
		if records[i].ID == "device-test-fix1" {
			records[i].State = "disabled"
		}
	}
	if err := env.devices.writeFile(records); err != nil {
		t.Fatalf("writeFile failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "device-test-fix1")

	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized for unknown device state, got %d", w.Code)
	}

	var errResp hub.ErrorBody
	if err := json.NewDecoder(w.Body).Decode(&errResp); err != nil {
		t.Fatalf("failed to decode error body: %v", err)
	}
	if errResp.Error.Code != hub.CodeUnauthorized {
		t.Fatalf("expected error code %q, got %q", hub.CodeUnauthorized, errResp.Error.Code)
	}
}

func TestFix4TouchLastSeenLogError(t *testing.T) {
	env := setupTestEnv(t, nil)
	env.approveDevice(t, "dev-approved-001", "pixel")

	env.now = env.now.Add(2 * time.Minute)

	lockPath := env.devPath + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("create lockfile failed: %v", err)
	}
	f.Close()
	if err := os.Chmod(lockPath, 0400); err != nil {
		t.Fatalf("chmod lockfile failed: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(lockPath, 0600)
	})

	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "dev-approved-001")
	req.Header.Set("X-Forwarded-For", "100.70.80.99")

	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	if len(env.logs) == 0 {
		t.Fatalf("expected TouchLastSeen error to be logged through cfg.Log, but env.logs is empty")
	}
	found := false
	for _, l := range env.logs {
		if strings.Contains(l, "touch last seen error") || strings.Contains(l, "permission denied") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected TouchLastSeen error in logs, got %v", env.logs)
	}
}

func TestFix5XForwardedForLastElement(t *testing.T) {
	env := setupTestEnv(t, nil)

	reqReg := httptest.NewRequest("GET", "/v1/hello", nil)
	reqReg.RemoteAddr = "127.0.0.1:40000"
	reqReg.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqReg.Header.Set("X-LW-Device", "device-test-fix5")
	reqReg.Header.Set("X-LW-Device-Name", "test-device")
	reqReg.Header.Set("X-Forwarded-For", "203.0.113.1, 100.70.80.96")

	wReg := httptest.NewRecorder()
	env.handler.ServeHTTP(wReg, reqReg)
	if wReg.Code != http.StatusForbidden {
		t.Fatalf("expected 403 device_pending, got %d", wReg.Code)
	}

	rec, ok, err := env.devices.Get("device-test-fix5")
	if err != nil || !ok {
		t.Fatalf("Get device failed: %v", err)
	}
	if rec.LastAddr != "100.70.80.96" {
		t.Fatalf("registration: expected LastAddr %q, got %q", "100.70.80.96", rec.LastAddr)
	}

	if _, err := env.devices.Approve("device-test-fix5"); err != nil {
		t.Fatalf("Approve failed: %v", err)
	}
	env.now = env.now.Add(2 * time.Minute)

	reqSeen := httptest.NewRequest("GET", "/v1/hello", nil)
	reqSeen.RemoteAddr = "127.0.0.1:40000"
	reqSeen.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	reqSeen.Header.Set("X-LW-Device", "device-test-fix5")
	reqSeen.Header.Set("X-Forwarded-For", "198.51.100.2, 100.70.80.99")

	wSeen := httptest.NewRecorder()
	env.handler.ServeHTTP(wSeen, reqSeen)
	if wSeen.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", wSeen.Code)
	}

	rec, ok, err = env.devices.Get("device-test-fix5")
	if err != nil || !ok {
		t.Fatalf("Get device after touch failed: %v", err)
	}
	if rec.LastAddr != "100.70.80.99" {
		t.Fatalf("last seen: expected LastAddr %q, got %q", "100.70.80.99", rec.LastAddr)
	}
}

func TestFixO1CapDeviceName64Bytes(t *testing.T) {
	env := setupTestEnv(t, nil)

	longName := strings.Repeat("a", 63) + "\xe4\xb8\x96"

	req := httptest.NewRequest("GET", "/v1/hello", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Tailscale-User-Login", "awepo-pro@github")
	req.Header.Set("X-LW-Device", "device-test-fix-o1")
	req.Header.Set("X-LW-Device-Name", longName)

	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 device_pending, got %d", w.Code)
	}

	rec, ok, err := env.devices.Get("device-test-fix-o1")
	if err != nil || !ok {
		t.Fatalf("Get device failed: %v", err)
	}

	if len([]byte(rec.Name)) > 64 {
		t.Fatalf("expected device Name <= 64 bytes, got %d bytes: %q", len([]byte(rec.Name)), rec.Name)
	}
	expectedName := strings.Repeat("a", 63)
	if rec.Name != expectedName {
		t.Fatalf("expected device Name %q, got %q", expectedName, rec.Name)
	}
}
