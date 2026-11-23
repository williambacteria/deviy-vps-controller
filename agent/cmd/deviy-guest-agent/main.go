package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

type server struct {
	token       string
	allowedCIDR *net.IPNet
}

func main() {
	listen := flag.String("listen", "0.0.0.0:8099", "guest agent listen address")
	token := flag.String("token", "", "optional guest agent bearer token")
	allow := flag.String("allow", "192.168.122.1/32", "allowed host CIDR")
	flag.Parse()

	_, allowed, err := net.ParseCIDR(*allow)
	if err != nil {
		log.Fatalf("invalid --allow CIDR: %v", err)
	}

	app := &server{token: *token, allowedCIDR: allowed}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", app.auth(app.health))
	mux.HandleFunc("/password", app.auth(app.password))
	mux.HandleFunc("/network", app.auth(app.network))
	mux.HandleFunc("/dns", app.auth(app.dns))
	mux.HandleFunc("/datadisk", app.auth(app.datadisk))
	mux.HandleFunc("/resize", app.auth(app.resize))
	mux.HandleFunc("/reboot", app.auth(app.reboot))
	mux.HandleFunc("/hostname", app.auth(app.hostname))

	go bootstrapFromSMBIOS()
	log.Printf("Deviy Guest Agent listening on %s (os=%s)", *listen, runtime.GOOS)
	httpServer := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func (s *server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "source not allowed"})
			return
		}
		sourceIP := net.ParseIP(host)
		if sourceIP == nil || (!sourceIP.IsLoopback() && !s.allowedCIDR.Contains(sourceIP)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "source not allowed"})
			return
		}
		provided := r.Header.Get("X-Deviy-Token")
		if s.token != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		next(w, r)
	}
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "deviy-guest-agent", "os": runtime.GOOS})
}

func (s *server) password(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !usernamePattern.MatchString(input.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid username"})
		return
	}
	if len(input.Password) < 8 || strings.ContainsAny(input.Password, "\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password must be at least 8 chars and cannot contain newlines"})
		return
	}
	if err := applyPassword(input.Username, input.Password); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "password_updated", "username": input.Username})
}

func (s *server) network(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		IP      string `json:"ip"`
		Prefix  int    `json:"prefix"`
		Gateway string `json:"gateway"`
		DNS1    string `json:"dns1"`
		DNS2    string `json:"dns2"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if net.ParseIP(input.IP).To4() == nil || input.Prefix < 1 || input.Prefix > 32 || net.ParseIP(input.Gateway).To4() == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid network configuration"})
		return
	}
	for _, dns := range []string{input.DNS1, input.DNS2} {
		if strings.TrimSpace(dns) != "" && net.ParseIP(strings.TrimSpace(dns)).To4() == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid dns configuration"})
			return
		}
	}
	if err := configureNetwork(input.IP, input.Prefix, input.Gateway, input.DNS1, input.DNS2); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "network_updated", "ip": input.IP})
}

func (s *server) dns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		DNS1 string `json:"dns1"`
		DNS2 string `json:"dns2"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if strings.TrimSpace(input.DNS1) == "" && strings.TrimSpace(input.DNS2) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "dns is required"})
		return
	}
	for _, dns := range []string{input.DNS1, input.DNS2} {
		if strings.TrimSpace(dns) != "" && net.ParseIP(strings.TrimSpace(dns)).To4() == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid dns configuration"})
			return
		}
	}
	if err := applyDNS(input.DNS1, input.DNS2); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dns_updated", "dns1": input.DNS1, "dns2": input.DNS2})
}

func (s *server) resize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	details, err := resizeSystemDisk()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "system_disk_resized", "details": strings.TrimSpace(details)})
}

func (s *server) datadisk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	details, err := ensureDataDisks()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "data_disks_ready", "details": strings.TrimSpace(details)})
}

func (s *server) reboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := rebootSystem(); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "reboot_started"})
}

func (s *server) hostname(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || !usernamePattern.MatchString(input.Hostname) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid hostname"})
		return
	}
	if err := setHostname(input.Hostname); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": strings.TrimSpace(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "hostname_updated", "hostname": input.Hostname})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
