package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type ticket struct {
	ID          int64     `json:"id"`
	Passenger   string    `json:"passenger"`
	Flight      string    `json:"flight"`
	Origin      string    `json:"origin"`
	Destination string    `json:"destination"`
	Departure   time.Time `json:"departure"`
	Status      string    `json:"status"`
}
type app struct {
	db         *pgxpool.Pool
	secret     []byte
	localSetup bool
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any) {
	respondStatus(w, http.StatusOK, value)
}
func respondStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func (a *app) token() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	payload := fmt.Sprintf("%d.%s", time.Now().Add(8*time.Hour).Unix(), base64.RawURLEncoding.EncodeToString(nonce))
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
func (a *app) authorized(r *http.Request) bool {
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 2 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	payload, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	signature, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	if e1 != nil || e2 != nil {
		return false
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return false
	}
	fields := strings.Split(string(payload), ".")
	if len(fields) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(fields[0], 10, 64)
	return err == nil && expiry > time.Now().Unix()
}
func valid(t ticket) bool {
	return len(strings.TrimSpace(t.Passenger)) > 0 && len(t.Passenger) <= 200 && len(strings.TrimSpace(t.Flight)) > 0 && len(t.Flight) <= 30 && len(strings.TrimSpace(t.Origin)) > 0 && len(t.Origin) <= 100 && len(strings.TrimSpace(t.Destination)) > 0 && len(t.Destination) <= 100 && !t.Departure.IsZero() && (t.Status == "booked" || t.Status == "cancelled")
}
func (a *app) handler() http.Handler {
	mux := http.NewServeMux()
	metrics := &httpMetrics{
		series: make(map[metricLabels]*metricValue),
		logger: slog.New(slog.NewJSONHandler(os.Stdout, nil)),
	}
	mux.HandleFunc("GET /metrics", metrics.serve)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var migrationsReady bool
		if err := a.db.QueryRow(ctx, "SELECT to_regclass('tickets') IS NOT NULL AND to_regclass('admins') IS NOT NULL").Scan(&migrationsReady); err != nil || !migrationsReady {
			http.Error(w, "database unavailable or migrations pending", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/setup/status", func(w http.ResponseWriter, r *http.Request) {
		if !a.localSetup {
			respond(w, map[string]bool{"needsSetup": false})
			return
		}
		var exists bool
		if err := a.db.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM admins)").Scan(&exists); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		respond(w, map[string]bool{"needsSetup": !exists})
	})
	mux.HandleFunc("POST /api/setup", func(w http.ResponseWriter, r *http.Request) {
		if !a.localSetup {
			http.Error(w, "setup disabled", 403)
			return
		}
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decode(w, r, &input) {
			return
		}
		input.Username = strings.TrimSpace(input.Username)
		if input.Username == "" || len(input.Username) > 200 || len(input.Password) < 12 || len(input.Password) > 72 {
			http.Error(w, "invalid credentials", 400)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "setup unavailable", 500)
			return
		}
		var id int
		err = a.db.QueryRow(r.Context(), "INSERT INTO admins (id, username, password_hash) VALUES (1, $1, $2) ON CONFLICT DO NOTHING RETURNING id", input.Username, string(hash)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "admin already exists", 409)
			return
		}
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		respondStatus(w, 201, map[string]string{"username": input.Username})
	})
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if !decode(w, r, &input) {
			return
		}
		var username, hash string
		err := a.db.QueryRow(r.Context(), "SELECT username, password_hash FROM admins WHERE id = 1").Scan(&username, &hash)
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "invalid credentials", 401)
			return
		}
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		if input.Username != username || bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil {
			http.Error(w, "invalid credentials", 401)
			return
		}
		token, err := a.token()
		if err != nil {
			http.Error(w, "token unavailable", 500)
			return
		}
		respond(w, map[string]string{"token": token})
	})
	mux.HandleFunc("/api/tickets", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		switch r.Method {
		case http.MethodGet:
			rows, err := a.db.Query(r.Context(), "SELECT id, passenger, flight, origin, destination, departure, status FROM tickets ORDER BY id DESC LIMIT 500")
			if err != nil {
				http.Error(w, "database error", 500)
				return
			}
			defer rows.Close()
			result := []ticket{}
			for rows.Next() {
				var t ticket
				if err := rows.Scan(&t.ID, &t.Passenger, &t.Flight, &t.Origin, &t.Destination, &t.Departure, &t.Status); err != nil {
					http.Error(w, "database error", 500)
					return
				}
				result = append(result, t)
			}
			if rows.Err() != nil {
				http.Error(w, "database error", 500)
				return
			}
			respond(w, result)
		case http.MethodPost:
			var t ticket
			if !decode(w, r, &t) {
				return
			}
			t.Status = "booked"
			if !valid(t) {
				http.Error(w, "invalid ticket", 400)
				return
			}
			err := a.db.QueryRow(r.Context(), "INSERT INTO tickets (passenger,flight,origin,destination,departure,status) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id", t.Passenger, t.Flight, t.Origin, t.Destination, t.Departure, t.Status).Scan(&t.ID)
			if err != nil {
				http.Error(w, "database error", 500)
				return
			}
			respondStatus(w, http.StatusCreated, t)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", 405)
		}
	})
	mux.HandleFunc("/api/tickets/", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(r) {
			http.Error(w, "unauthorized", 401)
			return
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/api/tickets/"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid id", 400)
			return
		}
		switch r.Method {
		case http.MethodPut:
			var t ticket
			if !decode(w, r, &t) {
				return
			}
			if t.Status != "booked" {
				http.Error(w, "invalid status", 400)
				return
			}
			if !valid(t) {
				http.Error(w, "invalid ticket", 400)
				return
			}
			tag, err := a.db.Exec(r.Context(), "UPDATE tickets SET passenger=$1,flight=$2,origin=$3,destination=$4,departure=$5 WHERE id=$6 AND status='booked'", t.Passenger, t.Flight, t.Origin, t.Destination, t.Departure, id)
			if err != nil {
				http.Error(w, "database error", 500)
				return
			}
			if tag.RowsAffected() == 0 {
				http.Error(w, "ticket not found or cancelled", 404)
				return
			}
			t.ID = id
			respond(w, t)
		case http.MethodDelete:
			tag, err := a.db.Exec(r.Context(), "UPDATE tickets SET status='cancelled' WHERE id=$1 AND status='booked'", id)
			if err != nil {
				http.Error(w, "database error", 500)
				return
			}
			if tag.RowsAffected() == 0 {
				http.Error(w, "ticket not found or cancelled", 404)
				return
			}
			w.WriteHeader(204)
		default:
			w.Header().Set("Allow", "PUT, DELETE")
			http.Error(w, "method not allowed", 405)
		}
	})
	return metrics.instrument(mux)
}

// Labels come only from registered patterns and a fixed method vocabulary.
// No URL, query, header, or body data is retained.
type metricLabels struct{ method, route, status string }
type metricValue struct {
	count   uint64
	sum     float64
	buckets [8]uint64
}

var durationBounds = [...]float64{.005, .01, .025, .05, .1, .5, 1, 5}

type httpMetrics struct {
	logger *slog.Logger
	mu     sync.Mutex
	series map[metricLabels]*metricValue
}
type metricWriter struct {
	http.ResponseWriter
	status int
}

func (w *metricWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *metricWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != 101 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *metricWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
func (m *httpMetrics) instrument(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		writer := &metricWriter{ResponseWriter: w}
		_, route := mux.Handler(r)
		mux.ServeHTTP(writer, r)
		if route == "" {
			route = "unmatched"
		}
		if i := strings.IndexByte(route, ' '); i >= 0 {
			route = route[i+1:]
		}
		method := r.Method
		switch method {
		case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
		default:
			method = "OTHER"
		}
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		elapsed := time.Since(start).Seconds()
		m.mu.Lock()
		key := metricLabels{method, route, strconv.Itoa(status)}
		v := m.series[key]
		if v == nil {
			v = &metricValue{}
			m.series[key] = v
		}
		v.count++
		v.sum += elapsed
		for i, bound := range durationBounds {
			if elapsed <= bound {
				v.buckets[i]++
			}
		}
		m.mu.Unlock()
		if m.logger != nil {
			m.logger.Info("http_request", "method", method, "route", route, "status", status, "duration_seconds", elapsed)
		}
	})
}
func (m *httpMetrics) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	m.mu.Lock()
	snapshot := make(map[metricLabels]metricValue, len(m.series))
	for key, value := range m.series {
		snapshot[key] = *value
	}
	m.mu.Unlock()
	fmt.Fprintln(w, "# HELP http_requests_total Completed HTTP requests.\n# TYPE http_requests_total counter")
	fmt.Fprintln(w, "# HELP http_request_duration_seconds HTTP request duration in seconds.\n# TYPE http_request_duration_seconds histogram")
	for key, v := range snapshot {
		labels := fmt.Sprintf("method=%q,route=%q,status=%q", key.method, key.route, key.status)
		fmt.Fprintf(w, "http_requests_total{%s} %d\n", labels, v.count)
		for i, bound := range durationBounds {
			fmt.Fprintf(w, "http_request_duration_seconds_bucket{%s,le=%q} %d\n", labels, strconv.FormatFloat(bound, 'g', -1, 64), v.buckets[i])
		}
		fmt.Fprintf(w, "http_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\nhttp_request_duration_seconds_sum{%s} %g\nhttp_request_duration_seconds_count{%s} %d\n", labels, v.count, labels, v.sum, labels, v.count)
	}
}

func listenAddress(configured string) string {
	if configured == "" {
		return ":8080"
	}
	return configured
}
func main() {
	secret, url := os.Getenv("TOKEN_SECRET"), os.Getenv("DATABASE_URL")
	if len(secret) < 32 || url == "" {
		log.Fatal("TOKEN_SECRET (32+ bytes) and DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	address := listenAddress(os.Getenv("LISTEN_ADDR"))
	a := &app{db: db, secret: []byte(secret), localSetup: os.Getenv("ENABLE_LOCAL_SETUP") == "true"}
	log.Fatal(http.ListenAndServe(address, a.handler()))
}
