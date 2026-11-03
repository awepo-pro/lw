package hubserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"time"

	"github.com/awepo-pro/lw/internal/hub"
)

var reDeviceID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

type Config struct {
	Users       []string
	DevicesPath string
	Now         func() time.Time
	Log         func(format string, args ...any)
}

type server struct {
	svc     hub.Service
	cfg     Config
	devices *Devices
}

func New(svc hub.Service, cfg Config) (http.Handler, error) {
	if svc == nil {
		return nil, errors.New("hubserver: nil service")
	}
	if cfg.DevicesPath == "" {
		return nil, errors.New("hubserver: empty DevicesPath")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	d, err := OpenDevices(cfg.DevicesPath)
	if err != nil {
		return nil, err
	}
	return &server{
		svc:     svc,
		cfg:     cfg,
		devices: d,
	}, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Loopback check
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		writeError(w, http.StatusUnauthorized, hub.CodeUnauthorized, "unauthorized: loopback remote address required")
		return
	}

	// 2. Tailscale user login check
	user := r.Header.Get("Tailscale-User-Login")
	if user == "" || !isAllowedUser(user, s.cfg.Users) {
		writeError(w, http.StatusUnauthorized, hub.CodeUnauthorized, "unauthorized user")
		return
	}

	// 3. X-LW-Device header check
	deviceID := r.Header.Get("X-LW-Device")
	if !reDeviceID.MatchString(deviceID) {
		writeError(w, http.StatusBadRequest, hub.CodeBadRequest, "bad_request: invalid or missing X-LW-Device")
		return
	}

	// 4. Device state in devices store
	rec, found, err := s.devices.Get(deviceID)
	if err != nil {
		s.cfg.Log("hubserver: get device error: %v", err)
		writeError(w, http.StatusInternalServerError, hub.CodeInternal, "internal error; see the hub log")
		return
	}
	now := s.cfg.Now()
	if !found {
		name := r.Header.Get("X-LW-Device-Name")
		if name == "" {
			name = "phone"
		}
		remoteAddr := r.Header.Get("X-Forwarded-For")
		_, err := s.devices.RegisterPending(deviceID, name, user, remoteAddr, now)
		if err != nil {
			s.cfg.Log("hubserver: register pending error: %v", err)
			writeError(w, http.StatusInternalServerError, hub.CodeInternal, "internal error; see the hub log")
			return
		}
		writeError(w, http.StatusForbidden, hub.CodeDevicePending, "device pending approval")
		return
	}

	if rec.State == hub.DevicePending {
		writeError(w, http.StatusForbidden, hub.CodeDevicePending, "device pending approval")
		return
	}
	if rec.State == hub.DeviceRevoked {
		writeError(w, http.StatusForbidden, hub.CodeDeviceRevoked, "device revoked")
		return
	}
	if rec.State != hub.DeviceApproved {
		writeError(w, http.StatusForbidden, hub.CodeUnauthorized, "device not approved")
		return
	}

	// Approved device: touch LastSeen at most once a minute
	remoteAddr := r.Header.Get("X-Forwarded-For")
	goTouch := true
	if rec.LastSeen != "" {
		if t, err := time.Parse(time.RFC3339, rec.LastSeen); err == nil {
			if now.Sub(t) < time.Minute && now.Sub(t) >= 0 {
				goTouch = false
			}
		}
	}
	if goTouch {
		_, _ = s.devices.TouchLastSeen(deviceID, remoteAddr, now)
	}

	// 5. Routes dispatch
	switch r.URL.Path {
	case "/v1/hello":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, hub.CodeBadRequest, "method not allowed")
			return
		}
		s.handleHello(w, r)
	case "/v1/manifest":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, hub.CodeBadRequest, "method not allowed")
			return
		}
		s.handleManifest(w, r)
	case "/v1/file":
		s.handleFile(w, r, rec)
	case "/v1/changes":
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, hub.CodeBadRequest, "method not allowed")
			return
		}
		s.handleChanges(w, r)
	case "/v1/status":
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, hub.CodeBadRequest, "method not allowed")
			return
		}
		s.handleStatus(w, r, rec)
	default:
		writeError(w, http.StatusNotFound, hub.CodeNotFound, "route not found")
	}
}

func (s *server) handleHello(w http.ResponseWriter, r *http.Request) {
	hello, err := s.svc.Hello(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	hello.Device = hub.DeviceApproved
	writeJSON(w, http.StatusOK, hello)
}

func (s *server) handleManifest(w http.ResponseWriter, r *http.Request) {
	manifest, err := s.svc.Manifest(r.Context())
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	if manifest.Files == nil {
		manifest.Files = []hub.FileEntry{}
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *server) handleChanges(w http.ResponseWriter, r *http.Request) {
	since := r.URL.Query().Get("since")
	changes, err := s.svc.Changes(r.Context(), since)
	if err != nil {
		s.handleServiceError(w, err)
		return
	}
	if changes.Changed == nil {
		changes.Changed = []hub.Change{}
	}
	writeJSON(w, http.StatusOK, changes)
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request, dev DeviceRecord) {
	body, err := io.ReadAll(io.LimitReader(r.Body, hub.MaxStatusBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, hub.CodeBadRequest, "failed to read status body")
		return
	}
	if int64(len(body)) > hub.MaxStatusBytes {
		writeError(w, http.StatusRequestEntityTooLarge, hub.CodeTooLarge, "status report body too large")
		return
	}

	var report hub.StatusReport
	if err := json.Unmarshal(body, &report); err != nil {
		writeError(w, http.StatusBadRequest, hub.CodeBadRequest, "invalid json")
		return
	}

	if err := s.devices.SetStatus(dev.ID, &report); err != nil {
		s.cfg.Log("hubserver: SetStatus error: %v", err)
		writeError(w, http.StatusInternalServerError, hub.CodeInternal, "internal error; see the hub log")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleFile(w http.ResponseWriter, r *http.Request, devRec DeviceRecord) {
	dev := hub.Device{
		ID:   devRec.ID,
		Name: devRec.Slug,
	}

	switch r.Method {
	case http.MethodGet:
		path := r.URL.Query().Get("path")
		data, sha, err := s.svc.Read(r.Context(), path)
		if err != nil {
			s.handleServiceError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-LW-SHA256", sha)
		w.Header().Set("ETag", fmt.Sprintf("%q", sha))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)

	case http.MethodPut:
		path := r.URL.Query().Get("path")
		base := r.URL.Query().Get("base")

		body, err := io.ReadAll(io.LimitReader(r.Body, hub.MaxBodyBytes+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, hub.CodeBadRequest, "failed to read body")
			return
		}
		if int64(len(body)) > hub.MaxBodyBytes {
			writeError(w, http.StatusRequestEntityTooLarge, hub.CodeTooLarge, "body too large")
			return
		}

		res, err := s.svc.Put(r.Context(), dev, path, base, body)
		if err != nil {
			s.handleServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)

	case http.MethodDelete:
		path := r.URL.Query().Get("path")
		base := r.URL.Query().Get("base")

		res, err := s.svc.Delete(r.Context(), dev, path, base)
		if err != nil {
			s.handleServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, res)

	default:
		writeError(w, http.StatusMethodNotAllowed, hub.CodeBadRequest, "method not allowed")
	}
}

func (s *server) handleServiceError(w http.ResponseWriter, err error) {
	var hubErr *hub.Error
	if errors.As(err, &hubErr) {
		status := httpStatusForCode(hubErr.Code)
		writeError(w, status, hubErr.Code, hubErr.Message)
		return
	}
	s.cfg.Log("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, hub.CodeInternal, "internal error; see the hub log")
}

func isAllowedUser(user string, allowed []string) bool {
	for _, u := range allowed {
		if u == user {
			return true
		}
	}
	return false
}

func httpStatusForCode(code string) int {
	switch code {
	case hub.CodeBadRequest:
		return http.StatusBadRequest // 400
	case hub.CodeUnauthorized:
		return http.StatusUnauthorized // 401
	case hub.CodeOutOfScope:
		return http.StatusForbidden // 403
	case hub.CodeDevicePending:
		return http.StatusForbidden // 403
	case hub.CodeDeviceRevoked:
		return http.StatusForbidden // 403
	case hub.CodeNotFound:
		return http.StatusNotFound // 404
	case hub.CodeCaseCollision:
		return http.StatusConflict // 409
	case hub.CodeTooLarge:
		return http.StatusRequestEntityTooLarge // 413
	case hub.CodeInternal:
		return http.StatusInternalServerError // 500
	case hub.CodeDiverged:
		return http.StatusServiceUnavailable // 503
	default:
		return http.StatusInternalServerError // 500
	}
}

func writeJSON(w http.ResponseWriter, status int, val any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(val)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(hub.ErrorBody{
		Error: hub.APIError{
			Code:    code,
			Message: message,
		},
	})
}
