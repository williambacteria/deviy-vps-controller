package controller

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	controllerSessionCookie = "deviy_controller_session"
	controllerSessionTTL    = 24 * time.Hour
)

type loginFailure struct {
	count       int
	blockedTill time.Time
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *Controller) ConfigureAuth(username, password, apiToken string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.adminUser = strings.TrimSpace(username)
	c.adminPassword = password
	c.apiToken = strings.TrimSpace(apiToken)
	if c.sessions == nil {
		c.sessions = map[string]time.Time{}
	}
	if c.loginFailures == nil {
		c.loginFailures = map[string]loginFailure{}
	}
}

func (c *Controller) authEnabled() bool {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.adminPassword != "" || c.apiToken != ""
}

func (c *Controller) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.authEnabled() || isPublicControllerPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/vncws") && c.validVNCTicket(r.URL.Path, r.URL.Query().Get("ticket")) {
			next.ServeHTTP(w, r)
			return
		}
		if c.apiToken != "" && strings.HasPrefix(r.URL.Path, "/api/") {
			provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(c.apiToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		if c.hasSession(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
			return
		}
		nextPath := r.URL.RequestURI()
		http.Redirect(w, r, "/login?next="+url.QueryEscape(nextPath), http.StatusSeeOther)
	})
}

func isPublicControllerPath(path string) bool {
	if strings.HasPrefix(path, "/novnc/") {
		return true
	}
	switch path {
	case "/health", "/login", "/api/login", "/logout", "/favicon.ico", "/devicloud-logo-white.png", "/devicloud-logo-light.png", "/api/agent/register", "/api/agent/heartbeat":
		return true
	}

	return false
}

func (c *Controller) hasSession(r *http.Request) bool {
	cookie, err := r.Cookie(controllerSessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	c.authMu.Lock()
	defer c.authMu.Unlock()
	expires, ok := c.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(c.sessions, cookie.Value)
		return false
	}

	return true
}

func (c *Controller) loginPage(w http.ResponseWriter, r *http.Request) {
	if c.hasSession(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data, err := webFS.ReadFile("web/login.html")
	if err != nil {
		http.Error(w, "login page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (c *Controller) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ip := remoteIP(r)
	if blocked, remaining := c.loginBlocked(ip); blocked {
		w.Header().Set("Retry-After", remaining)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "尝试次数过多，请稍后再试"})
		return
	}

	var input loginRequest
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式错误"})
			return
		}
	} else {
		_ = r.ParseForm()
		input.Username = r.Form.Get("username")
		input.Password = r.Form.Get("password")
	}

	c.authMu.Lock()
	expectedUser := c.adminUser
	expectedPassword := c.adminPassword
	c.authMu.Unlock()
	if expectedPassword == "" || subtle.ConstantTimeCompare([]byte(input.Username), []byte(expectedUser)) != 1 ||
		subtle.ConstantTimeCompare([]byte(input.Password), []byte(expectedPassword)) != 1 {
		c.recordLoginFailure(ip)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "用户名或密码错误"})
		return
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "会话创建失败"})
		return
	}
	token := hex.EncodeToString(tokenBytes)
	c.authMu.Lock()
	if c.sessions == nil {
		c.sessions = map[string]time.Time{}
	}
	c.sessions[token] = time.Now().Add(controllerSessionTTL)
	delete(c.loginFailures, ip)
	c.authMu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     controllerSessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(controllerSessionTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *Controller) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(controllerSessionCookie); err == nil {
		c.authMu.Lock()
		delete(c.sessions, cookie.Value)
		c.authMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     controllerSessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (c *Controller) loginBlocked(ip string) (bool, string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	failure, ok := c.loginFailures[ip]
	if !ok || failure.blockedTill.IsZero() {
		return false, ""
	}
	if time.Now().After(failure.blockedTill) {
		delete(c.loginFailures, ip)
		return false, ""
	}

	return true, "60"
}

func (c *Controller) recordLoginFailure(ip string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	if c.loginFailures == nil {
		c.loginFailures = map[string]loginFailure{}
	}
	failure := c.loginFailures[ip]
	failure.count++
	if failure.count >= 8 {
		failure.blockedTill = time.Now().Add(5 * time.Minute)
		failure.count = 0
	}
	c.loginFailures[ip] = failure
}

func remoteIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		if net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}

	return r.RemoteAddr
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
