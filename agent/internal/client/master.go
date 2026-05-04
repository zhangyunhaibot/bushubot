package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Client struct {
	baseURL    string
	licenseKey string
	http       *http.Client
}

func New(baseURL, licenseKey string) *Client {
	return &Client{
		baseURL:    baseURL,
		licenseKey: licenseKey,
		http:       &http.Client{Timeout: 15 * time.Second},
	}
}

type HeartbeatRequest struct {
	CurrentVersion string  `json:"current_version"`
	AgentVersion   string  `json:"agent_version"`
	Hostname       string  `json:"hostname"`
	ServerIP       string  `json:"server_ip"`
	MemUsedMB      int     `json:"mem_used_mb,omitempty"`
	MemTotalMB     int     `json:"mem_total_mb,omitempty"`
	DiskUsedGB     int     `json:"disk_used_gb,omitempty"`
	DiskTotalGB    int     `json:"disk_total_gb,omitempty"`
	Load1m         float64 `json:"load_1m,omitempty"`
	CPUCount       int     `json:"cpu_count,omitempty"`
	UptimeSeconds  int64   `json:"uptime_seconds,omitempty"`
}

type HeartbeatResponse struct {
	LatestVersion            string `json:"latest_version"`
	DownloadURL              string `json:"download_url"`
	LatestAgentVersion       string `json:"latest_agent_version,omitempty"`
	AgentDownloadURL         string `json:"agent_download_url,omitempty"`
	MinSupportedAgentVersion string `json:"min_supported_agent_version,omitempty"`
	Enabled                  bool   `json:"enabled"`
	Message                  string `json:"message"`
}

func (c *Client) Heartbeat(req HeartbeatRequest) (*HeartbeatResponse, error) {
	var out HeartbeatResponse
	if err := c.do("POST", "/api/v1/agent/heartbeat", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type ReportRequest struct {
	EventType    string `json:"event_type"`
	Version      string `json:"version"`
	ErrorMessage string `json:"error_message,omitempty"`
}

func (c *Client) Report(req ReportRequest) error {
	return c.do("POST", "/api/v1/agent/report", req, nil)
}

type Notification struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Version string `json:"version"`
	Message string `json:"message"`
}

type NotificationsResponse struct {
	Notifications []Notification `json:"notifications"`
}

func (c *Client) Notifications() (*NotificationsResponse, error) {
	var out NotificationsResponse
	if err := c.do("GET", "/api/v1/agent/notifications", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

type LogsRequest struct {
	Service string `json:"service"`
	Content string `json:"content"`
	Bytes   int    `json:"bytes"`
}

func (c *Client) PostLogs(req LogsRequest) error {
	return c.do("POST", "/api/v1/agent/logs", req, nil)
}

type ackRequest struct {
	IDs []int64 `json:"ids"`
}

// AckNotification 通知 master "已成功执行此条 notification"，
// master 收到后才把它标 delivered。
// 老 master（无此 endpoint）会返回 404，但响应已经在拉取时 attempts++ 兜底，不会丢。
func (c *Client) AckNotification(id int64) error {
	return c.do("POST", "/api/v1/agent/notifications/ack", ackRequest{IDs: []int64{id}}, nil)
}

func (c *Client) do(method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.licenseKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("master 返回 %d: %s", resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, out)
	}
	return nil
}
