// Package direct defines the browser-free receiver protocol.
package direct

import "time"

type Config struct {
	Server     string `json:"server"`
	Token      string `json:"token"`
	DeviceName string `json:"deviceName"`
}

type File struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Job struct {
	ID        string    `json:"id"`
	DeviceID  string    `json:"deviceId"`
	Files     []File    `json:"files"`
	Status    string    `json:"status"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func Active(status string) bool {
	return status == "queued" || status == "awaiting_accept" || status == "receiving"
}
