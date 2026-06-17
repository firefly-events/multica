package hive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const bridgeStatusStaleAfter = 12 * time.Second

func bridgeStatusFilePath() string {
	if path := strings.TrimSpace(os.Getenv("MULTICA_HERMES_BRIDGE_STATUS_PATH")); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "multica-hermes-bridge-status.json")
}

type bridgeStatusSnapshot struct {
	WorkspaceID string                             `json:"workspace_id"`
	UpdatedAt   string                             `json:"updated_at"`
	Bridge      bridgeConnectionStatus             `json:"bridge"`
	Threads     map[string]bridgeThreadStatusEntry `json:"threads"`
}

type bridgeConnectionStatus struct {
	Connected     bool   `json:"connected"`
	UpdatedAt     string `json:"updated_at"`
	LastHeartbeat string `json:"last_heartbeat_at,omitempty"`
	LastEventAt   string `json:"last_event_at,omitempty"`
	LastConnectAt string `json:"last_connect_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type bridgeThreadStatusEntry struct {
	State     string `json:"state"`
	MessageID string `json:"message_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

type HermesBridgeStatusResponse struct {
	Bridge HermesBridgeConnectionStatus `json:"bridge"`
	Thread HermesBridgeThreadStatus     `json:"thread"`
}

type HermesBridgeConnectionStatus struct {
	Connected     bool   `json:"connected"`
	Stale         bool   `json:"stale"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	LastHeartbeat string `json:"last_heartbeat_at,omitempty"`
	LastEventAt   string `json:"last_event_at,omitempty"`
	LastConnectAt string `json:"last_connect_at,omitempty"`
	LastError     string `json:"last_error,omitempty"`
}

type HermesBridgeThreadStatus struct {
	State     string `json:"state"`
	MessageID string `json:"message_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

func loadBridgeStatusSnapshot(path string) (bridgeStatusSnapshot, error) {
	var snap bridgeStatusSnapshot
	data, err := os.ReadFile(path)
	if err != nil {
		return snap, err
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return snap, fmt.Errorf("decode bridge status: %w", err)
	}
	if snap.Threads == nil {
		snap.Threads = map[string]bridgeThreadStatusEntry{}
	}
	return snap, nil
}

func readHermesBridgeStatus(workspaceID, threadID string) HermesBridgeStatusResponse {
	resp := HermesBridgeStatusResponse{
		Bridge: HermesBridgeConnectionStatus{Connected: false, Stale: true},
		Thread: HermesBridgeThreadStatus{State: "unknown"},
	}

	snap, err := loadBridgeStatusSnapshot(bridgeStatusFilePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			resp.Bridge.LastError = err.Error()
		}
		return resp
	}
	if snap.WorkspaceID != "" && workspaceID != "" && snap.WorkspaceID != workspaceID {
		resp.Bridge.LastError = "bridge status belongs to another workspace"
		return resp
	}

	resp.Bridge.Connected = snap.Bridge.Connected
	resp.Bridge.UpdatedAt = snap.Bridge.UpdatedAt
	resp.Bridge.LastHeartbeat = snap.Bridge.LastHeartbeat
	resp.Bridge.LastEventAt = snap.Bridge.LastEventAt
	resp.Bridge.LastConnectAt = snap.Bridge.LastConnectAt
	resp.Bridge.LastError = snap.Bridge.LastError
	resp.Bridge.Stale = bridgeHeartbeatStale(snap.Bridge.LastHeartbeat)

	if threadID != "" {
		if thread, ok := snap.Threads[threadID]; ok {
			resp.Thread = HermesBridgeThreadStatus{
				State:     thread.State,
				MessageID: thread.MessageID,
				StartedAt: thread.StartedAt,
				UpdatedAt: thread.UpdatedAt,
				Error:     thread.Error,
			}
		} else {
			resp.Thread.State = "idle"
		}
	}

	return resp
}

func bridgeHeartbeatStale(lastHeartbeat string) bool {
	if strings.TrimSpace(lastHeartbeat) == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, lastHeartbeat)
	if err != nil {
		return true
	}
	return time.Since(ts) > bridgeStatusStaleAfter
}
