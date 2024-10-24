package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sync"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

type Endpoint struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type DowntimeRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Duration  string    `json:"duration"`
	Reason    string    `json:"reason"`
}

type StatusRecord struct {
	Timestamp    time.Time `json:"timestamp"`
	IsUp         bool      `json:"isUp"`
	ResponseTime float64   `json:"responseTime"`
	StatusCode   int       `json:"statusCode,omitempty"`
	Error        string    `json:"error,omitempty"`
}

type EndpointStatus struct {
	Name           string           `json:"name"`
	URL            string           `json:"url"`
	CurrentStatus  string           `json:"currentStatus"`
	LastChecked    time.Time        `json:"lastChecked"`
	History        []StatusRecord   `json:"history"`
	RecentDowntime []DowntimeRecord `json:"recentDowntime"`
}

var (
	endpoints      = make(map[string]Endpoint)
	endpointStatus = make(map[string]*EndpointStatus)
	mu             sync.RWMutex
	// Changed check interval to 30 seconds for testing
	checkInterval = 30 * time.Second
	historyWindow = 10 * time.Hour
)

func main() {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("/api/endpoint", handleEndpoint)
	mux.HandleFunc("/api/status", handleCORS(getStatus))
	mux.HandleFunc("/", serveDashboard)

	// Start the monitoring goroutine
	go monitorEndpoints()

	fmt.Println("Server starting on :8080...")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func handleCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next(w, r)
	}
}

func serveDashboard(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFS(templateFS, "templates/index.html")
	if err != nil {
		http.Error(w, "Failed to load template", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	tmpl.Execute(w, nil)
}

func handleEndpoint(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == http.MethodPost {
		var endpoint Endpoint
		if err := json.NewDecoder(r.Body).Decode(&endpoint); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		mu.Lock()
		endpoints[endpoint.Name] = endpoint
		endpointStatus[endpoint.Name] = &EndpointStatus{
			Name:           endpoint.Name,
			URL:            endpoint.URL,
			CurrentStatus:  "Pending",
			LastChecked:    time.Now(),
			History:        make([]StatusRecord, 0),
			RecentDowntime: make([]DowntimeRecord, 0),
		}
		mu.Unlock()

		// Immediately check the new endpoint
		go checkEndpoint(endpoint)

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(endpoint)
	}
}

func getStatus(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	defer mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(endpointStatus)
}

func monitorEndpoints() {
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		checkAllEndpoints()
		<-ticker.C
	}
}

func checkAllEndpoints() {
	mu.RLock()
	endpointsCopy := make(map[string]Endpoint)
	for name, endpoint := range endpoints {
		endpointsCopy[name] = endpoint
	}
	mu.RUnlock()

	for _, endpoint := range endpointsCopy {
		checkEndpoint(endpoint)
	}
}

func checkEndpoint(endpoint Endpoint) {
	start := time.Now()
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Get(endpoint.URL)
	responseTime := time.Since(start).Seconds()

	status := StatusRecord{
		Timestamp:    time.Now(),
		ResponseTime: responseTime,
	}

	var downtime *DowntimeRecord
	if err != nil {
		status.IsUp = false
		status.Error = err.Error()
		downtime = &DowntimeRecord{
			Timestamp: time.Now(),
			Duration:  "ongoing",
			Reason:    err.Error(),
		}
	} else {
		status.StatusCode = resp.StatusCode
		status.IsUp = resp.StatusCode < 400
		if !status.IsUp {
			downtime = &DowntimeRecord{
				Timestamp: time.Now(),
				Duration:  "ongoing",
				Reason:    fmt.Sprintf("HTTP Status %d", resp.StatusCode),
			}
		}
		resp.Body.Close()
	}

	mu.Lock()
	if s, exists := endpointStatus[endpoint.Name]; exists {
		s.LastChecked = status.Timestamp
		s.CurrentStatus = getStatusString(status.IsUp)
		s.History = append(s.History, status)

		if downtime != nil {
			if len(s.RecentDowntime) > 0 {
				lastDowntime := &s.RecentDowntime[len(s.RecentDowntime)-1]
				if lastDowntime.Duration == "ongoing" {
					duration := time.Since(lastDowntime.Timestamp)
					lastDowntime.Duration = duration.Round(time.Second).String()
				}
			}
			s.RecentDowntime = append(s.RecentDowntime, *downtime)
			if len(s.RecentDowntime) > 5 {
				s.RecentDowntime = s.RecentDowntime[len(s.RecentDowntime)-5:]
			}
		}

		// Cleanup old history
		cutoff := time.Now().Add(-historyWindow)
		newHistory := []StatusRecord{}
		for _, record := range s.History {
			if record.Timestamp.After(cutoff) {
				newHistory = append(newHistory, record)
			}
		}
		s.History = newHistory
	}
	mu.Unlock()
}

func getStatusString(isUp bool) string {
	if isUp {
		return "UP"
	}
	return "DOWN"
}
