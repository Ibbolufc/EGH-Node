package main

import (
	"encoding/json"
	"net/http"
	"runtime"
)

type SystemInfo struct {
	Version       string `json:"version"`
	Architecture  string `json:"architecture"`
	OS            string `json:"os"`
	CPUCount      int    `json:"cpu_count"`
	KernelVersion string `json:"kernel_version"`
	MemoryTotal   int64  `json:"memory_total"`
}

func systemHandler(w http.ResponseWriter, r *http.Request) {
	info := SystemInfo{
		Version:       "egh-node-v1",
		Architecture:  runtime.GOARCH,
		OS:            runtime.GOOS,
		CPUCount:      runtime.NumCPU(),
		KernelVersion: "unknown",
		MemoryTotal:   0,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}
