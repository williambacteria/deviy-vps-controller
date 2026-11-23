package controller

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestControllerLoginSessionAndBearerToken(t *testing.T) {
	app := New(nil)
	app.ConfigureAuth("admin", "correct-password", "machine-token")
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(resp.Header.Get("Location"), "/login") {
		t.Fatalf("expected login redirect, got %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/login", strings.NewReader(`{"username":"admin","password":"wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, server.URL+"/api/login", strings.NewReader(`{"username":"admin","password":"correct-password"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 || cookies[0].Name != controllerSessionCookie || !cookies[0].HttpOnly {
		t.Fatalf("unexpected session cookie: %+v", cookies)
	}

	req, _ = http.NewRequest(http.MethodGet, server.URL+"/api/nodes", nil)
	req.AddCookie(cookies[0])
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session API status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, server.URL+"/api/nodes", nil)
	req.Header.Set("Authorization", "Bearer machine-token")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer API status = %d", resp.StatusCode)
	}
	var payload struct {
		Items []any `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
}

func TestControllerLoginPage(t *testing.T) {
	app := New(nil)
	app.ConfigureAuth("admin", "password", "")
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	resp, err := http.Get(server.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login page status = %d", resp.StatusCode)
	}
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(page), "登录控制台") {
		t.Fatalf("unexpected login page")
	}
}
