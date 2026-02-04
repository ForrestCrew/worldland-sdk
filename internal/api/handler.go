package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/worldland/worldland-node/internal/rental"
)

// StartRentalRequest is the JSON body for POST /rentals/start
type StartRentalRequest struct {
	SessionID    string `json:"sessionId"`
	GPUDeviceID  string `json:"gpuDeviceId"`  // NVIDIA UUID
	Image        string `json:"image"`        // Container image
	SSHPublicKey string `json:"sshPublicKey"`
	MemoryBytes  int64  `json:"memoryBytes"`
	CPUCount     int64  `json:"cpuCount"`
}

// StartRentalResponse is returned on successful start
type StartRentalResponse struct {
	SessionID  string `json:"sessionId"`
	SSHHost    string `json:"sshHost"`
	SSHPort    int    `json:"sshPort"`
	SSHUser    string `json:"sshUser"`
	SSHCommand string `json:"sshCommand"`
}

// StopRentalRequest is the JSON body for POST /rentals/stop
type StopRentalRequest struct {
	SessionID string `json:"sessionId"`
}

// StopRentalResponse is returned on successful stop
type StopRentalResponse struct {
	SessionID string `json:"sessionId"`
	Message   string `json:"message"`
}

// ErrorResponse for error cases
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// RentalExecutorInterface defines operations needed from rental executor
type RentalExecutorInterface interface {
	StartRental(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error)
	StopRental(ctx context.Context, sessionID string) error
	GetRentalStatus(sessionID string) (*rental.RentalState, error)
}

// RentalHandler handles HTTP requests for rental operations
type RentalHandler struct {
	executor RentalExecutorInterface
	hostAddr string // Node's public address for SSH connections
}

// NewRentalHandler creates a new rental handler
func NewRentalHandler(executor RentalExecutorInterface, hostAddr string) *RentalHandler {
	return &RentalHandler{
		executor: executor,
		hostAddr: hostAddr,
	}
}

// HandleStartRental handles POST /rentals/start
func (h *RentalHandler) HandleStartRental(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	var req StartRentalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	// Validate required fields
	if req.SessionID == "" {
		h.writeError(w, http.StatusBadRequest, "sessionId is required", "MISSING_SESSION_ID")
		return
	}
	if req.GPUDeviceID == "" {
		h.writeError(w, http.StatusBadRequest, "gpuDeviceId is required", "MISSING_GPU_DEVICE_ID")
		return
	}
	if req.SSHPublicKey == "" {
		h.writeError(w, http.StatusBadRequest, "sshPublicKey is required", "MISSING_SSH_KEY")
		return
	}

	// Default values per CONTEXT.md
	if req.Image == "" {
		req.Image = "nvidia/cuda:12.1.1-runtime-ubuntu22.04"
	}
	if req.MemoryBytes == 0 {
		req.MemoryBytes = 16 * 1024 * 1024 * 1024 // 16GB default
	}
	if req.CPUCount == 0 {
		req.CPUCount = 8 // 8 CPUs default
	}

	// Execute rental start
	execReq := rental.StartRentalRequest{
		SessionID:    req.SessionID,
		GPUDeviceID:  req.GPUDeviceID,
		Image:        req.Image,
		SSHPublicKey: req.SSHPublicKey,
		MemoryBytes:  req.MemoryBytes,
		CPUCount:     req.CPUCount,
		Host:         h.hostAddr,
	}

	connInfo, err := h.executor.StartRental(r.Context(), execReq)
	if err != nil {
		if errors.Is(err, rental.ErrSessionAlreadyActive) {
			h.writeError(w, http.StatusConflict, "rental already exists", "RENTAL_EXISTS")
			return
		}
		if errors.Is(err, rental.ErrContainerNotHealthy) {
			h.writeError(w, http.StatusServiceUnavailable, "container failed to start", "CONTAINER_NOT_READY")
			return
		}
		h.writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL_ERROR")
		return
	}

	resp := StartRentalResponse{
		SessionID:  req.SessionID,
		SSHHost:    connInfo.Host,
		SSHPort:    connInfo.Port,
		SSHUser:    connInfo.User,
		SSHCommand: connInfo.Command,
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// HandleStopRental handles POST /rentals/stop
func (h *RentalHandler) HandleStopRental(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	var req StopRentalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body", "INVALID_REQUEST")
		return
	}

	if req.SessionID == "" {
		h.writeError(w, http.StatusBadRequest, "sessionId is required", "MISSING_SESSION_ID")
		return
	}

	if err := h.executor.StopRental(r.Context(), req.SessionID); err != nil {
		if errors.Is(err, rental.ErrSessionNotFound) {
			h.writeError(w, http.StatusNotFound, "rental not found", "RENTAL_NOT_FOUND")
			return
		}
		h.writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL_ERROR")
		return
	}

	resp := StopRentalResponse{
		SessionID: req.SessionID,
		Message:   "rental stopped, container will be cleaned up after grace period",
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// HandleGetStatus handles GET /rentals/status?sessionId=xxx
func (h *RentalHandler) HandleGetStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed", "METHOD_NOT_ALLOWED")
		return
	}

	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, "sessionId query param required", "MISSING_SESSION_ID")
		return
	}

	state, err := h.executor.GetRentalStatus(sessionID)
	if err != nil {
		if errors.Is(err, rental.ErrSessionNotFound) {
			h.writeError(w, http.StatusNotFound, "rental not found", "RENTAL_NOT_FOUND")
			return
		}
		h.writeError(w, http.StatusInternalServerError, err.Error(), "INTERNAL_ERROR")
		return
	}

	h.writeJSON(w, http.StatusOK, state)
}

// writeJSON writes a JSON response
func (h *RentalHandler) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeError writes an error response
func (h *RentalHandler) writeError(w http.ResponseWriter, status int, message, code string) {
	h.writeJSON(w, status, ErrorResponse{Error: message, Code: code})
}
