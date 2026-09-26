package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListenAddress(t *testing.T) {
	if got := listenAddress(""); got != ":8080" {
		t.Fatalf("default address = %q, want :8080", got)
	}
	if got := listenAddress("127.0.0.1:9090"); got != "127.0.0.1:9090" {
		t.Fatalf("configured address = %q", got)
	}
}

func TestCreatedJSONResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	respondStatus(rec, 201, map[string]int{"id": 1})
	response := rec.Result()
	defer response.Body.Close()
	if response.StatusCode != 201 || response.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("status = %d, content type = %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if rec.Body.String() != "{\"id\":1}\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestAuthAndAPI(t *testing.T) {
	a := &app{secret: []byte(strings.Repeat("x", 32))}
	h := a.handler()
	check := func(method, path, body, auth string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
		}
		return rec
	}
	check("GET", "/healthz", "", "", 200)
	check("GET", "/api/tickets", "", "", 401)
	check("POST", "/api/setup", `{"username":"admin","password":"long-test-password"}`, "", 403)
	check("GET", "/api/setup/status", "", "", 200)
	check("POST", "/api/login", `{"username":"admin","password":"test-password","extra":true}`, "", 400)
	check("GET", "/api/tickets", "", "invalid", 401)
}

func TestFirstAdminIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL to run the PostgreSQL first-admin integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1 // Keep the temporary admins table on the same database session.
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	migration, err := os.ReadFile("002_admin.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, strings.Replace(string(migration), "CREATE TABLE", "CREATE TEMP TABLE", 1)); err != nil {
		t.Fatalf("apply admin migration as a temporary table: %v", err)
	}
	a := &app{db: db, secret: []byte(strings.Repeat("x", 32)), localSetup: true}
	h := a.handler()
	check := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, rec.Code, want, rec.Body.String())
		}
		return rec
	}
	// The database may already contain the persistent tickets migration.
	check("GET", "/readyz", "", 200)
	if body := check("GET", "/api/setup/status", "", 200).Body.String(); body != "{\"needsSetup\":true}\n" {
		t.Fatalf("initial status: %s", body)
	}
	check("POST", "/api/setup", `{"username":"admin","password":"short"}`, 400)
	check("POST", "/api/setup", `{"username":"admin","password":"long-test-password"}`, 201)
	if body := check("GET", "/api/setup/status", "", 200).Body.String(); body != "{\"needsSetup\":false}\n" {
		t.Fatalf("registered status: %s", body)
	}
	check("POST", "/api/setup", `{"username":"another","password":"long-test-password"}`, 409)
	check("POST", "/api/login", `{"username":"admin","password":"wrong-password"}`, 401)
	check("POST", "/api/login", `{"username":"another","password":"long-test-password"}`, 401)
	rec := check("POST", "/api/login", `{"username":"admin","password":"long-test-password"}`, 200)
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || result.Token == "" {
		t.Fatal("missing token", err)
	}
	req := httptest.NewRequest("GET", "/api/tickets", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	if !a.authorized(req) {
		t.Fatal("login token rejected")
	}
	req = httptest.NewRequest("PUT", "/api/tickets/invalid", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("invalid ticket id: got %d, want 400", rec.Code)
	}
}

func TestTicketValidation(t *testing.T) {
	item := ticket{Passenger: "P", Flight: "F", Origin: "A", Destination: "B", Departure: time.Now(), Status: "booked"}
	if !valid(item) {
		t.Fatal("valid ticket rejected")
	}
	item.Status = "unknown"
	if valid(item) {
		t.Fatal("invalid status accepted")
	}
}
func TestInvalidToken(t *testing.T) {
	a := &app{secret: []byte(strings.Repeat("x", 32))}
	token, err := a.token()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if !a.authorized(req) {
		t.Fatal("valid token rejected")
	}
	req.Header.Set("Authorization", "Bearer "+token+"x")
	if a.authorized(req) {
		t.Fatal("tampered token accepted")
	}
}
