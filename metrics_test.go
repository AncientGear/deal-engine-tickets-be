package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsEndpoint(t *testing.T) {
	h := (&app{}).handler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200},
		{"HEAD", "/healthz", 200},
		{"GET", "/api/tickets/private-id?token=secret-value", 401},
		{"GET", "/api/tickets/another-id", 401},
		{"GET", "/unknown-private-path", 404},
		{"POST", "/healthz", 405},
		{"CUSTOM-PRIVATE", "/unknown", 404},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader("private-body"))
			r.Header.Set("Authorization", "Bearer private-token")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
		})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("metrics response: %d %v", w.Code, w.Header())
	}
	body := w.Body.String()
	for _, want := range []string{
		`http_requests_total{method="GET",route="/healthz",status="200"} 1`,
		`http_requests_total{method="GET",route="/api/tickets/",status="401"} 2`,
		`http_requests_total{method="OTHER",route="unmatched",status="404"} 1`,
		`http_request_duration_seconds_bucket{method="GET",route="/healthz",status="200",le="+Inf"} 1`,
		`http_request_duration_seconds_count{method="GET",route="/healthz",status="200"} 1`,
		`http_request_duration_seconds_sum{method="GET",route="/healthz",status="200"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s in %s", want, body)
		}
	}
	for _, secret := range []string{"private-id", "another-id", "secret-value", "unknown-private-path", "private-body", "private-token", "CUSTOM-PRIVATE"} {
		if strings.Contains(body, secret) {
			t.Errorf("metrics leaked %q", secret)
		}
	}
}

func TestStructuredRequestLog(t *testing.T) {
	var output bytes.Buffer
	m := &httpMetrics{
		series: make(map[metricLabels]*metricValue),
		logger: slog.New(slog.NewJSONHandler(&output, nil)),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tickets/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	r := httptest.NewRequest("GET", "/api/tickets/private-id?token=private-query", strings.NewReader("private-body"))
	r.Header.Set("Authorization", "Bearer private-token")
	w := httptest.NewRecorder()
	m.instrument(mux).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["method"] != "GET" || entry["route"] != "/api/tickets/" || entry["status"] != float64(401) || entry["msg"] != "http_request" {
		t.Fatalf("unexpected log: %v", entry)
	}
	if duration, ok := entry["duration_seconds"].(float64); !ok || duration < 0 {
		t.Fatalf("invalid duration: %v", entry)
	}
	for key := range entry {
		switch key {
		case "time", "level", "msg", "method", "route", "status", "duration_seconds":
		default:
			t.Errorf("unexpected log field %q", key)
		}
	}
	if strings.Contains(output.String(), "private-") {
		t.Fatalf("request data leaked: %s", output.String())
	}
}

func TestMetricsRegistriesAreIndependent(t *testing.T) {
	for i := 0; i < 2; i++ {
		h := (&app{}).handler()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/healthz", nil))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
		if !strings.Contains(w.Body.String(), `http_requests_total{method="GET",route="/healthz",status="200"} 1`) {
			t.Fatal("handler metrics not isolated")
		}
	}
}
