package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/worldland/worldland-node/internal/rental"
)

// MockRentalExecutor for testing
type MockRentalExecutor struct {
	StartRentalFn     func(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error)
	StopRentalFn      func(ctx context.Context, sessionID string) error
	GetRentalStatusFn func(sessionID string) (*rental.RentalState, error)
}

func (m *MockRentalExecutor) StartRental(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error) {
	if m.StartRentalFn != nil {
		return m.StartRentalFn(ctx, req)
	}
	return nil, errors.New("StartRentalFn not implemented")
}

func (m *MockRentalExecutor) StopRental(ctx context.Context, sessionID string) error {
	if m.StopRentalFn != nil {
		return m.StopRentalFn(ctx, sessionID)
	}
	return errors.New("StopRentalFn not implemented")
}

func (m *MockRentalExecutor) GetRentalStatus(sessionID string) (*rental.RentalState, error) {
	if m.GetRentalStatusFn != nil {
		return m.GetRentalStatusFn(sessionID)
	}
	return nil, errors.New("GetRentalStatusFn not implemented")
}

func TestHandleStartRental_Success(t *testing.T) {
	mock := &MockRentalExecutor{
		StartRentalFn: func(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error) {
			return &rental.ConnectionInfo{
				Host:        "provider.example.com",
				Port:        30001,
				User:        "ubuntu",
				Command:     "ssh -p 30001 ubuntu@provider.example.com",
				ContainerID: "container-123",
			}, nil
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		SessionID:    "session-123",
		GPUDeviceID:  "GPU-uuid-456",
		SSHPublicKey: "ssh-rsa AAAA...",
		Image:        "nvidia/cuda:12.1-runtime-ubuntu22.04",
		MemoryBytes:  16 * 1024 * 1024 * 1024,
		CPUCount:     8,
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp StartRentalResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	assert.Equal(t, "session-123", resp.SessionID)
	assert.Equal(t, "provider.example.com", resp.SSHHost)
	assert.Equal(t, 30001, resp.SSHPort)
	assert.Equal(t, "ubuntu", resp.SSHUser)
	assert.Equal(t, "ssh -p 30001 ubuntu@provider.example.com", resp.SSHCommand)
}

func TestHandleStartRental_MissingSessionID_Returns400(t *testing.T) {
	mock := &MockRentalExecutor{}
	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		GPUDeviceID:  "GPU-uuid-456",
		SSHPublicKey: "ssh-rsa AAAA...",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "MISSING_SESSION_ID", errResp.Code)
}

func TestHandleStartRental_MissingGPUDeviceID_Returns400(t *testing.T) {
	mock := &MockRentalExecutor{}
	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		SessionID:    "session-123",
		SSHPublicKey: "ssh-rsa AAAA...",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "MISSING_GPU_DEVICE_ID", errResp.Code)
}

func TestHandleStartRental_MissingSSHKey_Returns400(t *testing.T) {
	mock := &MockRentalExecutor{}
	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		SessionID:   "session-123",
		GPUDeviceID: "GPU-uuid-456",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "MISSING_SSH_KEY", errResp.Code)
}

func TestHandleStartRental_DuplicateSession_Returns409(t *testing.T) {
	mock := &MockRentalExecutor{
		StartRentalFn: func(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error) {
			return nil, rental.ErrSessionAlreadyActive
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		SessionID:    "session-123",
		GPUDeviceID:  "GPU-uuid-456",
		SSHPublicKey: "ssh-rsa AAAA...",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "RENTAL_EXISTS", errResp.Code)
}

func TestHandleStartRental_ContainerNotReady_Returns503(t *testing.T) {
	mock := &MockRentalExecutor{
		StartRentalFn: func(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error) {
			return nil, rental.ErrContainerNotHealthy
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		SessionID:    "session-123",
		GPUDeviceID:  "GPU-uuid-456",
		SSHPublicKey: "ssh-rsa AAAA...",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "CONTAINER_NOT_READY", errResp.Code)
}

func TestHandleStartRental_DefaultValues(t *testing.T) {
	var capturedReq rental.StartRentalRequest
	mock := &MockRentalExecutor{
		StartRentalFn: func(ctx context.Context, req rental.StartRentalRequest) (*rental.ConnectionInfo, error) {
			capturedReq = req
			return &rental.ConnectionInfo{
				Host:        "provider.example.com",
				Port:        30001,
				User:        "ubuntu",
				Command:     "ssh -p 30001 ubuntu@provider.example.com",
				ContainerID: "container-123",
			}, nil
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StartRentalRequest{
		SessionID:    "session-123",
		GPUDeviceID:  "GPU-uuid-456",
		SSHPublicKey: "ssh-rsa AAAA...",
		// Omit Image, MemoryBytes, CPUCount to test defaults
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/start", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStartRental(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	// Verify defaults were applied
	assert.Equal(t, "nvidia/cuda:12.1-runtime-ubuntu22.04", capturedReq.Image)
	assert.Equal(t, int64(16*1024*1024*1024), capturedReq.MemoryBytes)
	assert.Equal(t, int64(8), capturedReq.CPUCount)
}

func TestHandleStopRental_Success(t *testing.T) {
	mock := &MockRentalExecutor{
		StopRentalFn: func(ctx context.Context, sessionID string) error {
			assert.Equal(t, "session-123", sessionID)
			return nil
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StopRentalRequest{
		SessionID: "session-123",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/stop", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStopRental(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp StopRentalResponse
	err := json.NewDecoder(rec.Body).Decode(&resp)
	require.NoError(t, err)

	assert.Equal(t, "session-123", resp.SessionID)
	assert.Contains(t, resp.Message, "grace period")
}

func TestHandleStopRental_NotFound_Returns404(t *testing.T) {
	mock := &MockRentalExecutor{
		StopRentalFn: func(ctx context.Context, sessionID string) error {
			return rental.ErrSessionNotFound
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	reqBody := StopRentalRequest{
		SessionID: "session-123",
	}

	body, _ := json.Marshal(reqBody)
	req := httptest.NewRequest(http.MethodPost, "/rentals/stop", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	handler.HandleStopRental(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "RENTAL_NOT_FOUND", errResp.Code)
}

func TestHandleGetStatus_Success(t *testing.T) {
	mock := &MockRentalExecutor{
		GetRentalStatusFn: func(sessionID string) (*rental.RentalState, error) {
			return &rental.RentalState{
				SessionID:   sessionID,
				ContainerID: "container-123",
				SSHPort:     30001,
			}, nil
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	req := httptest.NewRequest(http.MethodGet, "/rentals/status?sessionId=session-123", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetStatus(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var state rental.RentalState
	err := json.NewDecoder(rec.Body).Decode(&state)
	require.NoError(t, err)

	assert.Equal(t, "session-123", state.SessionID)
	assert.Equal(t, "container-123", state.ContainerID)
	assert.Equal(t, 30001, state.SSHPort)
}

func TestHandleGetStatus_NotFound_Returns404(t *testing.T) {
	mock := &MockRentalExecutor{
		GetRentalStatusFn: func(sessionID string) (*rental.RentalState, error) {
			return nil, rental.ErrSessionNotFound
		},
	}

	handler := NewRentalHandler(mock, "provider.example.com")

	req := httptest.NewRequest(http.MethodGet, "/rentals/status?sessionId=session-123", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetStatus(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var errResp ErrorResponse
	err := json.NewDecoder(rec.Body).Decode(&errResp)
	require.NoError(t, err)

	assert.Equal(t, "RENTAL_NOT_FOUND", errResp.Code)
}
