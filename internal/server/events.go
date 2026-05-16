package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) handleUIEvents(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, fmt.Errorf("streaming unsupported"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	writeEvent := func(name string, body []byte) bool {
		if _, err := fmt.Fprintf(w, "event: %s\n", name); err != nil {
			return false
		}
		if _, err := w.Write([]byte("data: ")); err != nil {
			return false
		}
		if _, err := w.Write(body); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\n\n")); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	snapshot := func() ([]byte, []byte, error) {
		data, err := s.buildUIData()
		if err != nil {
			return nil, nil, err
		}
		body, err := json.Marshal(data)
		if err != nil {
			return nil, nil, err
		}
		fingerprint, err := uiDataStreamFingerprint(data)
		if err != nil {
			return nil, nil, err
		}
		return body, fingerprint, nil
	}

	var last []byte
	sendSnapshot := func(force bool) bool {
		body, fingerprint, err := snapshot()
		if err != nil {
			payload, _ := json.Marshal(map[string]string{"message": err.Error()})
			return writeEvent("ui-error", payload)
		}
		if !force && bytes.Equal(fingerprint, last) {
			return true
		}
		last = append(last[:0], fingerprint...)
		return writeEvent("ui-data", body)
	}

	if !sendSnapshot(true) {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !sendSnapshot(false) {
				return
			}
		case <-heartbeat.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func uiDataStreamFingerprint(data uiData) ([]byte, error) {
	stable := data
	stable.Agents = append([]uiAgent(nil), data.Agents...)
	for i := range stable.Agents {
		stable.Agents[i].StartedMin = 0
		stable.Agents[i].LastActivitySec = 0
	}
	if data.DeadAgent != nil {
		dead := *data.DeadAgent
		dead.StartedMin = 0
		dead.LastActivitySec = 0
		stable.DeadAgent = &dead
	}
	stable.Playbooks = append([]uiPlaybook(nil), data.Playbooks...)
	for i := range stable.Playbooks {
		stable.Playbooks[i].LastMin = nil
	}
	stable.Workdirs = append([]uiWorkdir(nil), data.Workdirs...)
	for i := range stable.Workdirs {
		stable.Workdirs[i].UsedMin = 0
	}
	return json.Marshal(stable)
}
