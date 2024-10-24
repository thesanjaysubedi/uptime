package main

import (
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "sync"
    "time"
)

// Endpoint represents a service endpoint to monitor
type Endpoint struct {
    Name string `json:"name"`
    URL  string `json:"url"`
}

// DowntimeRecord stores information about a downtime incident
type DowntimeRecord struct {
    Timestamp time.Time `json:"timestamp"`
    Duration  string    `json:"duration"`
    Reason    string    `json:"reason"`
}

// StatusRecord represents a single status check
type StatusRecord struct {
    Timestamp    time.Time `json:"timestamp"`
    IsUp         bool      `json:"isUp"`
    ResponseTime float64   `json:"responseTime"`
    StatusCode   int       `json:"statusCode,omitempty"`
    Error        string    `json:"error,omitempty"`
}

// EndpointStatus represents the current and historical status of an endpoint
type EndpointStatus struct {
    Name           string           `json:"name"`
    URL            string           `json:"url"`
    CurrentStatus  string           `json:"currentStatus"`
    LastChecked    time.Time        `json:"lastChecked"`
    History        []StatusRecord   `json:"history"`
    RecentDowntime []DowntimeRecord `json:"recentDowntime"`
}

var (
    endpoints       = make(map[string]Endpoint)
    endpointStatus = make(map[string]*EndpointStatus)
    mu             sync.RWMutex
    checkInterval  = 30 * time.Minute
    historyWindow  = 10 * time.Hour
)

func main() {
    // API endpoints
    http.HandleFunc("/endpoint", handleEndpoint)
    http.HandleFunc("/status", getStatus)
    
    // Start the monitoring goroutine
    go monitorEndpoints()
    
    fmt.Println("Server starting on :8080...")
    log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleEndpoint(w http.ResponseWriter, r *http.Request) {
    switch r.Method {
    case http.MethodPost:
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
            History:        make([]StatusRecord, 0),
            RecentDowntime: make([]DowntimeRecord, 0),
        }
        mu.Unlock()
        
        w.WriteHeader(http.StatusCreated)
        json.NewEncoder(w).Encode(endpoint)
    }
}

func getStatus(w http.ResponseWriter, r *http.Request) {
    mu.RLock()
    defer mu.RUnlock()
    
    statusCopy := make(map[string]EndpointStatus)
    for name, status := range endpointStatus {
        // Filter history to last 10 hours
        cutoff := time.Now().Add(-historyWindow)
        filteredHistory := []StatusRecord{}
        for _, record := range status.History {
            if record.Timestamp.After(cutoff) {
                filteredHistory = append(filteredHistory, record)
            }
        }
        
        // Copy status with limited history
        statusCopy[name] = EndpointStatus{
            Name:           status.Name,
            URL:            status.URL,
            CurrentStatus:  status.CurrentStatus,
            LastChecked:    status.LastChecked,
            History:        filteredHistory,
            RecentDowntime: getLast5Downtimes(status.RecentDowntime),
        }
    }
    
    json.NewEncoder(w).Encode(statusCopy)
}

func getLast5Downtimes(downtimes []DowntimeRecord) []DowntimeRecord {
    if len(downtimes) <= 5 {
        return downtimes
    }
    return downtimes[len(downtimes)-5:]
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
    resp, err := http.Get(endpoint.URL)
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
        status.IsUp = true
        status.StatusCode = resp.StatusCode
        if resp.StatusCode >= 400 {
            status.IsUp = false
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
        s.LastChecked = time.Now()
        s.CurrentStatus = getStatusString(status.IsUp)
        s.History = append(s.History, status)
        
        // Add downtime record if service is down
        if downtime != nil {
            // Update duration of previous downtime if it exists
            if len(s.RecentDowntime) > 0 {
                lastDowntime := &s.RecentDowntime[len(s.RecentDowntime)-1]
                if lastDowntime.Duration == "ongoing" {
                    duration := time.Since(lastDowntime.Timestamp)
                    lastDowntime.Duration = duration.String()
                }
            }
            s.RecentDowntime = append(s.RecentDowntime, *downtime)
        }
        
        // Cleanup old history (older than 10 hours)
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