package main

const version = "2.3.0"

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed doc/openapi.yaml
var openAPISpec []byte

type contextKey string

const userContextKey contextKey = "user"

type LockConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`        // TOGGLE / STRIKE / PULSE
	HasBattery bool   `json:"has_battery"` // true / false
}

type ACLRule struct {
	User  string   `json:"user"`  // "*" for all users
	Locks []string `json:"locks"` // ["*"] or list of IDs
}

type Config struct {
	MQTT struct {
		Broker      string `json:"broker"`
		Port        int    `json:"port"`
		Username    string `json:"username"` // Base64 encoded
		Password    string `json:"password"` // Base64 encoded
		CAFile      string `json:"ca_file"`
		ClientID    string `json:"client_id"`
		TopicState  string `json:"topic_state"`   // locks/+/state
		TopicBatt   string `json:"topic_batt"`    // locks/+/batt
		TopicCmdTpl string `json:"topic_cmd_tpl"` // locks/%s/cmd
	} `json:"mqtt"`
	HTTP struct {
		Listen       string `json:"listen"`          // 127.0.0.1:8884
		AuthFile     string `json:"auth_file"`       // /etc/lockd2/auth_keys
		AuditFile    string `json:"audit_file"`      // /etc/lockd2/audit.log
		CertFile     string `json:"cert_file"`       // path to cert
		KeyFile      string `json:"key_file"`        // path to key
		ExternalURL  string `json:"external_url"`    // https://lockd.example.com:8884
		AdminKeyFile string `json:"admin_key_file"`  // /etc/lockd2/admin_key
		NtfyURL      string `json:"ntfy_url"`        // https://ntfy.sh/my-topic (empty = disabled)
	} `json:"http"`
	Locks []LockConfig `json:"locks"`
	ACL   []ACLRule    `json:"acl"`
}

type State struct {
	LockID    string    `json:"lock_id"`
	State     string    `json:"state"`               // Nyitva / Zárva / Ismeretlen...
	Battery   string    `json:"battery,omitempty"`   // %
	UpdatedAt time.Time `json:"updated_at"`          // server time
}

type LockResponse struct {
	LockConfig
	State     string    `json:"state,omitempty"`
	Battery   string    `json:"battery,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type Server struct {
	cfg     Config
	mc      mqtt.Client
	mu      sync.RWMutex
	state   map[string]State
	tlsCert *tls.Certificate // Cached certificate for reloading
}

const adminHTML = `<!DOCTYPE html>
<html lang="hu">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>lockd2 admin</title>
<style>
  body { font-family: sans-serif; max-width: 600px; margin: 40px auto; padding: 0 16px; color: #222; }
  h1 { font-size: 1.4rem; margin-bottom: 24px; }
  h2 { font-size: 1.1rem; margin-top: 32px; border-bottom: 1px solid #ddd; padding-bottom: 6px; }
  label { display: block; margin-bottom: 8px; font-size: .9rem; }
  input[type=text], input[type=password] {
    width: 100%; box-sizing: border-box; padding: 8px; border: 1px solid #ccc;
    border-radius: 4px; font-size: .95rem; margin-top: 4px;
  }
  button { margin-top: 10px; padding: 8px 18px; background: #2563eb; color: #fff;
    border: none; border-radius: 4px; cursor: pointer; font-size: .95rem; }
  button.danger { background: #dc2626; }
  button:hover { opacity: .88; }
  #result { margin-top: 14px; background: #f4f4f4; padding: 12px; border-radius: 4px;
    font-family: monospace; font-size: .85rem; white-space: pre; display: none; }
  #download { margin-top: 8px; display: none; }
  table { width: 100%; border-collapse: collapse; margin-top: 10px; }
  td, th { text-align: left; padding: 7px 10px; border-bottom: 1px solid #eee; font-size: .9rem; }
  th { background: #f8f8f8; font-weight: 600; }
  #msg { margin-top: 10px; font-size: .9rem; color: #dc2626; }
</style>
</head>
<body>
<h1>lockd2 admin</h1>

<label>Admin kulcs<input type="password" id="adminKey" placeholder="admin kulcs"></label>

<h2>Kulcs generálása</h2>
<label>Felhasználónév<input type="text" id="username" placeholder="pl. alice"></label>
<button onclick="genKey()">Generálás</button>
<div id="result"></div>
<a id="download" download="kulcs.json"><button type="button">Letöltés</button></a>

<h2>Felhasználók</h2>
<button onclick="loadKeys()">Frissítés</button>
<div id="msg"></div>
<table><thead><tr><th>Felhasználó</th><th></th></tr></thead>
<tbody id="keylist"></tbody></table>

<script>
function adminKey() { return document.getElementById('adminKey').value; }

async function genKey() {
  const username = document.getElementById('username').value.trim();
  if (!username) { alert('Add meg a felhasználónevet!'); return; }
  const res = await fetch('/v1/admin/gen-key', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Admin-Key': adminKey() },
    body: JSON.stringify({ username })
  });
  if (!res.ok) { alert('Hiba: ' + res.status + ' ' + await res.text()); return; }
  const data = await res.json();
  const json = JSON.stringify(data, null, 2);
  const el = document.getElementById('result');
  el.textContent = json;
  el.style.display = 'block';
  const blob = new Blob([json], { type: 'application/json' });
  const a = document.getElementById('download');
  a.href = URL.createObjectURL(blob);
  a.download = username + '.json';
  a.style.display = 'inline';
  loadKeys();
}

async function loadKeys() {
  const res = await fetch('/v1/admin/keys', { headers: { 'X-Admin-Key': adminKey() } });
  if (!res.ok) { document.getElementById('msg').textContent = 'Hiba: ' + res.status; return; }
  document.getElementById('msg').textContent = '';
  const { users } = await res.json();
  const tbody = document.getElementById('keylist');
  tbody.innerHTML = (users || []).map(u =>
    '<tr><td>' + u + '</td><td><button class="danger" onclick="delKey(\'' + u + '\')">Törlés</button></td></tr>'
  ).join('');
}

async function delKey(username) {
  if (!confirm('Törlöd ' + username + ' kulcsát?')) return;
  const res = await fetch('/v1/admin/keys/' + username, {
    method: 'DELETE', headers: { 'X-Admin-Key': adminKey() }
  });
  if (!res.ok) { alert('Hiba: ' + res.status); return; }
  loadKeys();
}
</script>
</body>
</html>`

func (s *Server) adminKeyHash() (string, error) {
	s.mu.RLock()
	f := s.cfg.HTTP.AdminKeyFile
	s.mu.RUnlock()
	if f == "" {
		return "", errors.New("admin_key_file nincs beállítva a configban")
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *Server) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-Admin-Key")
		if key == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		hRaw := sha256.Sum256([]byte(key))
		hash := hex.EncodeToString(hRaw[:])
		stored, err := s.adminKeyHash()
		if err != nil || stored == "" || hash != stored {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(adminHTML))
}

func handleSwagger(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html lang="hu">
<head>
<meta charset="UTF-8">
<title>lockd2 API docs</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist/swagger-ui-bundle.js"></script>
<script>
SwaggerUIBundle({
  url: "/openapi.yaml",
  dom_id: "#swagger-ui",
  presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
  layout: "BaseLayout",
  deepLinking: true,
  persistAuthorization: true
});

// Az auth dialog API key mezőit password típusúra állítja, hogy ne látsszon a kulcs
const observer = new MutationObserver(() => {
  document.querySelectorAll('.auth-container input[type="text"]').forEach(el => {
    el.type = 'password';
  });
});
observer.observe(document.body, { childList: true, subtree: true });
</script>
</body>
</html>`))
}

func handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openAPISpec)
}

func (s *Server) handleAdminGenKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct{ Username string `json:"username"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		http.Error(w, "missing username", http.StatusBadRequest)
		return
	}

	key := fmt.Sprintf("%d-%x", time.Now().UnixNano(), sha256.Sum256([]byte(req.Username)))
	key = key[:32]
	hRaw := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(hRaw[:])

	s.mu.RLock()
	authFile := s.cfg.HTTP.AuthFile
	externalURL := s.cfg.HTTP.ExternalURL
	s.mu.RUnlock()

	af, err := os.OpenFile(authFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		http.Error(w, "cannot open auth_keys", http.StatusInternalServerError)
		return
	}
	_, err = fmt.Fprintf(af, "%s:%s\n", req.Username, hash)
	af.Close()
	if err != nil {
		http.Error(w, "cannot write auth_keys", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": externalURL, "token": key})
}

func (s *Server) handleAdminKeys(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	authFile := s.cfg.HTTP.AuthFile
	s.mu.RUnlock()

	f, err := os.Open(authFile)
	if err != nil {
		http.Error(w, "cannot open auth_keys", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	users := []string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") { continue }
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 { users = append(users, parts[0]) }
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"users": users})
}

func (s *Server) handleAdminDeleteKey(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimPrefix(r.URL.Path, "/v1/admin/keys/")
	username = strings.Trim(username, "/")
	if username == "" {
		http.Error(w, "missing username", http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	authFile := s.cfg.HTTP.AuthFile
	s.mu.RUnlock()

	b, err := os.ReadFile(authFile)
	if err != nil {
		http.Error(w, "cannot read auth_keys", http.StatusInternalServerError)
		return
	}

	lines := strings.Split(string(b), "\n")
	out := make([]string, 0, len(lines))
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			out = append(out, line)
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) == 2 && parts[0] == username {
			found = true
			continue
		}
		out = append(out, line)
	}

	if !found {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	if err := os.WriteFile(authFile, []byte(strings.Join(out, "\n")), 0600); err != nil {
		http.Error(w, "cannot write auth_keys", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func decodeB64(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return s
	}
	return string(b)
}

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}

	if c.HTTP.Listen == "" {
		c.HTTP.Listen = "127.0.0.1:8884"
	}
	if c.HTTP.AuthFile == "" {
		c.HTTP.AuthFile = "/etc/lockd/auth_keys"
	}
	if c.HTTP.AuditFile == "" {
		c.HTTP.AuditFile = "/etc/lockd/audit.log"
	}

	c.MQTT.Username = decodeB64(c.MQTT.Username)
	c.MQTT.Password = decodeB64(c.MQTT.Password)

	return c, nil
}

func tlsConfigFromCA(caFile string) (*tls.Config, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pem); !ok {
		return nil, errors.New("CA PEM parse failed")
	}
	return &tls.Config{
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: false,
	}, nil
}

func (s *Server) updateStateFromTopic(topic, payload string) {
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		return
	}
	lockID := parts[1]
	suffix := parts[2]

	s.mu.Lock()
	st, ok := s.state[lockID]
	if !ok {
		st = State{LockID: lockID}
	}

	var stateChanged bool
	if suffix == "state" {
		stateChanged = st.State != payload && payload != ""
		st.State = payload
	} else if suffix == "batt" {
		st.Battery = payload
	}
	st.UpdatedAt = time.Now()
	s.state[lockID] = st
	s.mu.Unlock()

	if stateChanged {
		s.sendNtfy(lockID, payload)
	}
}

func (s *Server) sendNtfy(lockID, newState string) {
	s.mu.RLock()
	ntfyURL := s.cfg.HTTP.NtfyURL
	lockName := lockID
	for _, lc := range s.cfg.Locks {
		if lc.ID == lockID {
			lockName = lc.Name
			break
		}
	}
	s.mu.RUnlock()

	if ntfyURL == "" {
		return
	}

	msg := fmt.Sprintf("%s: %s", lockName, newState)
	go func() {
		req, err := http.NewRequest(http.MethodPost, ntfyURL, strings.NewReader(msg))
		if err != nil {
			log.Printf("ntfy: request error: %v", err)
			return
		}
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Title", "Lockd2")
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("ntfy: send error: %v", err)
			return
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("ntfy: HTTP %d for lock %s", resp.StatusCode, lockID)
		}
	}()
}

func (s *Server) reloadCert() error {
	s.mu.RLock()
	certFile := s.cfg.HTTP.CertFile
	keyFile := s.cfg.HTTP.KeyFile
	s.mu.RUnlock()

	if certFile == "" || keyFile == "" {
		return nil
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.tlsCert = &cert
	s.mu.Unlock()
	return nil
}

func (s *Server) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.tlsCert == nil {
		return nil, errors.New("no certificate loaded")
	}
	return s.tlsCert, nil
}

func (s *Server) mqttConnect() {
	tlsCfg, err := tlsConfigFromCA(s.cfg.MQTT.CAFile)
	if err != nil {
		log.Fatalf("tls ca load failed: %v", err)
	}

	brokerURL := fmt.Sprintf("ssl://%s:%d", s.cfg.MQTT.Broker, s.cfg.MQTT.Port)

	opts := mqtt.NewClientOptions().
		AddBroker(brokerURL).
		SetClientID(s.cfg.MQTT.ClientID).
		SetUsername(s.cfg.MQTT.Username).
		SetPassword(s.cfg.MQTT.Password).
		SetTLSConfig(tlsCfg).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(3 * time.Second).
		SetOnConnectHandler(func(c mqtt.Client) {
			log.Printf("MQTT connected (as %s): %s", s.cfg.MQTT.ClientID, brokerURL)
			c.Subscribe(s.cfg.MQTT.TopicState, 1, func(_ mqtt.Client, m mqtt.Message) {
				s.updateStateFromTopic(m.Topic(), string(m.Payload()))
			})
			c.Subscribe(s.cfg.MQTT.TopicBatt, 1, func(_ mqtt.Client, m mqtt.Message) {
				s.updateStateFromTopic(m.Topic(), string(m.Payload()))
			})
		})

	s.mc = mqtt.NewClient(opts)
	if tok := s.mc.Connect(); tok.Wait() && tok.Error() != nil {
		log.Fatalf("MQTT connect failed: %v", tok.Error())
	}
}

func (s *Server) auditLog(user, lockID, cmd string) {
	msg := fmt.Sprintf("%s | User: %s | Lock: %s | Cmd: %s\n", 
		time.Now().Format("2006-01-02 15:04:05"), user, lockID, cmd)
	
	f, err := os.OpenFile(s.cfg.HTTP.AuditFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("audit log error: %v", err)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(msg)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" { key = r.URL.Query().Get("key") }
		if key == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		hashBytes := sha256.Sum256([]byte(key))
		keyHash := hex.EncodeToString(hashBytes[:])

		f, err := os.Open(s.cfg.HTTP.AuthFile)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer f.Close()

		username := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") { continue }
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && parts[1] == keyHash {
				username = parts[0]
				break
			}
		}

		if username == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) canAccess(user, lockID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.cfg.ACL) == 0 {
		return true
	}

	for _, rule := range s.cfg.ACL {
		if rule.User == user || rule.User == "*" {
			for _, id := range rule.Locks {
				if id == lockID || id == "*" {
					return true
				}
			}
		}
	}
	return false
}

func (s *Server) handleLocks(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	user, _ := r.Context().Value(userContextKey).(string)

	out := make([]LockResponse, 0, len(s.cfg.Locks))
	for _, lc := range s.cfg.Locks {
		if !s.canAccess(user, lc.ID) {
			continue
		}
		lr := LockResponse{LockConfig: lc}
		if st, ok := s.state[lc.ID]; ok {
			lr.State = st.State
			lr.Battery = st.Battery
			lr.UpdatedAt = st.UpdatedAt
		}
		out = append(out, lr)
	}
	
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"locks": out})
}

func (s *Server) handleCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	p := strings.TrimPrefix(r.URL.Path, "/v1/locks/")
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[1] != "cmd" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]

	var req struct { Cmd string `json:"cmd"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	cmd := strings.ToUpper(strings.TrimSpace(req.Cmd))

	user, _ := r.Context().Value(userContextKey).(string)
	if !s.canAccess(user, id) {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	// Find lock config for validation
	s.mu.RLock()
	var targetLock *LockConfig
	for i := range s.cfg.Locks {
		if s.cfg.Locks[i].ID == id { targetLock = &s.cfg.Locks[i]; break }
	}
	s.mu.RUnlock()

	if targetLock == nil {
		http.Error(w, "unknown lock", http.StatusNotFound)
		return
	}

	// Guard: no LOCK for non-TOGGLE types (STRIKE, PULSE, OPEN)
	if targetLock.Type != "TOGGLE" && cmd == "LOCK" {
		http.Error(w, "LOCK not supported for this type", http.StatusBadRequest)
		return
	}

	// HTTP API uses "OPEN"; HA addon expects "UNLOCK" on MQTT
	mqttCmd := cmd
	if mqttCmd == "OPEN" {
		mqttCmd = "UNLOCK"
	}

	topic := fmt.Sprintf(s.cfg.MQTT.TopicCmdTpl, id)
	tok := s.mc.Publish(topic, 1, false, mqttCmd)
	tok.Wait()
	if tok.Error() != nil {
		http.Error(w, tok.Error().Error(), http.StatusInternalServerError)
		return
	}

	// Audit log using the user we already extracted
	s.auditLog(user, id, cmd)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func main() {
	var (
		cfgPath  = flag.String("config", "/etc/lockd2/lockd2.json", "Path to the JSON configuration file")
		encode   = flag.String("encode", "", "Helper to Base64 encode a string (useful for MQTT username/password in config)")
		genKey      = flag.String("gen-key", "", "Generate a new API key for a username, append to auth_keys and write <username>.json")
		testAuth    = flag.String("testauth", "", "Test whether the token in <username>.json matches the auth_keys entry")
		setAdminKey = flag.String("set-admin-key", "", "Hash and store an admin key in the admin_key_file configured in the config")
		discover    = flag.Bool("discover", false, "Connect to MQTT and print all incoming messages (Ctrl+C to stop)")
	)
	flag.Parse()

	if *encode != "" {
		fmt.Println(base64.StdEncoding.EncodeToString([]byte(*encode)))
		return
	}

	if *genKey != "" {
		key := fmt.Sprintf("%d-%x", time.Now().UnixNano(), sha256.Sum256([]byte(*genKey)))
		key = key[:32]
		hRaw := sha256.Sum256([]byte(key))
		hash := hex.EncodeToString(hRaw[:])

		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			log.Fatalf("config error (needed for auth_file path): %v", err)
		}

		_ = os.MkdirAll(filepath.Dir(cfg.HTTP.AuthFile), 0755)
		af, err := os.OpenFile(cfg.HTTP.AuthFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatalf("cannot open auth_keys %s: %v", cfg.HTTP.AuthFile, err)
		}
		_, err = fmt.Fprintf(af, "%s:%s\n", *genKey, hash)
		af.Close()
		if err != nil {
			log.Fatalf("cannot write auth_keys: %v", err)
		}

		clientCfg := map[string]string{"url": cfg.HTTP.ExternalURL, "token": key}
		b, _ := json.MarshalIndent(clientCfg, "", "  ")
		jsonFile := *genKey + ".json"
		if err := os.WriteFile(jsonFile, append(b, '\n'), 0600); err != nil {
			log.Fatalf("cannot write client json: %v", err)
		}

		fmt.Println("--- NEW API KEY GENERATED ---")
		fmt.Printf("User:          %s\n", *genKey)
		fmt.Printf("Raw Key:       %s\n", key)
		fmt.Printf("Auth keys:     %s  <-- sor hozzáfűzve\n", cfg.HTTP.AuthFile)
		fmt.Printf("Client config: %s  <-- add át a kliensnek\n", jsonFile)
		fmt.Println("-----------------------------")
		return
	}

	cfg, err := loadConfig(*cfgPath)
	if err != nil { log.Fatalf("config error: %v", err) }

	if *setAdminKey != "" {
		if cfg.HTTP.AdminKeyFile == "" {
			log.Fatalf("admin_key_file nincs beállítva a configban")
		}
		hRaw := sha256.Sum256([]byte(*setAdminKey))
		hash := hex.EncodeToString(hRaw[:])
		_ = os.MkdirAll(filepath.Dir(cfg.HTTP.AdminKeyFile), 0755)
		if err := os.WriteFile(cfg.HTTP.AdminKeyFile, []byte(hash+"\n"), 0600); err != nil {
			log.Fatalf("cannot write admin_key_file: %v", err)
		}
		fmt.Printf("Admin kulcs beállítva: %s\n", cfg.HTTP.AdminKeyFile)
		return
	}

	if *testAuth != "" {
		// Read client JSON
		b, err := os.ReadFile(*testAuth)
		if err != nil {
			fmt.Printf("HIBA: nem olvasható a fájl: %v\n", err)
			os.Exit(1)
		}
		var clientCfg map[string]string
		if err := json.Unmarshal(b, &clientCfg); err != nil {
			fmt.Printf("HIBA: érvénytelen JSON: %v\n", err)
			os.Exit(1)
		}
		token, ok := clientCfg["token"]
		if !ok || token == "" {
			fmt.Println("HIBA: nincs 'token' mező a JSON-ban")
			os.Exit(1)
		}

		// Hash the token
		hRaw := sha256.Sum256([]byte(token))
		hash := hex.EncodeToString(hRaw[:])

		// Search auth_keys
		f, err := os.Open(cfg.HTTP.AuthFile)
		if err != nil {
			fmt.Printf("HIBA: nem olvasható az auth_keys (%s): %v\n", cfg.HTTP.AuthFile, err)
			os.Exit(1)
		}
		defer f.Close()

		found := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") { continue }
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && parts[1] == hash {
				found = parts[0]
				break
			}
		}

		if found != "" {
			fmt.Printf("OK: a token érvényes, felhasználó: %s\n", found)
		} else {
			fmt.Printf("HIBA: a token hash (%s) nem szerepel az auth_keys-ben (%s)\n", hash, cfg.HTTP.AuthFile)
			os.Exit(1)
		}
		return
	}

	if *discover {
		tlsCfg, err := tlsConfigFromCA(cfg.MQTT.CAFile)
		if err != nil {
			log.Fatalf("tls ca load failed: %v", err)
		}
		brokerURL := fmt.Sprintf("ssl://%s:%d", cfg.MQTT.Broker, cfg.MQTT.Port)
		opts := mqtt.NewClientOptions().
			AddBroker(brokerURL).
			SetClientID(cfg.MQTT.ClientID+"-discover").
			SetUsername(cfg.MQTT.Username).
			SetPassword(cfg.MQTT.Password).
			SetTLSConfig(tlsCfg)
		mc := mqtt.NewClient(opts)
		if tok := mc.Connect(); tok.Wait() && tok.Error() != nil {
			log.Fatalf("MQTT connect failed: %v", tok.Error())
		}
		fmt.Printf("Csatlakozva: %s – várok üzenetekre (Ctrl+C a kilépéshez)...\n\n", brokerURL)
		mc.Subscribe("#", 0, func(_ mqtt.Client, m mqtt.Message) {
			fmt.Printf("%-40s  %s\n", m.Topic(), m.Payload())
		})
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		mc.Disconnect(250)
		return
	}

	// Ensure AuthFile exists (create dir and file if missing)
	if cfg.HTTP.AuthFile != "" {
		_ = os.MkdirAll(filepath.Dir(cfg.HTTP.AuthFile), 0755)
		if _, err := os.Stat(cfg.HTTP.AuthFile); os.IsNotExist(err) {
			log.Printf("Auth file missing, creating: %s", cfg.HTTP.AuthFile)
			_ = os.WriteFile(cfg.HTTP.AuthFile, []byte("# lockd2 auth keys\n"), 0600)
		}
	}
	// Ensure AuditFile directory exists
	if cfg.HTTP.AuditFile != "" {
		_ = os.MkdirAll(filepath.Dir(cfg.HTTP.AuditFile), 0755)
	}

	s := &Server{
		cfg:   cfg,
		state: make(map[string]State),
	}
	s.mqttConnect()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.mc == nil || !s.mc.IsConnected() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("mqtt disconnected\n"))
			return
		}
		_, _ = w.Write([]byte("ok lockd2/" + version + "\n"))
	})

	api := http.NewServeMux()
	api.HandleFunc("/v1/locks", s.handleLocks)
	api.HandleFunc("/v1/locks/", func(w http.ResponseWriter, r *http.Request) {
		// e.g. /v1/locks/frontdoor/cmd
		if strings.HasSuffix(r.URL.Path, "/cmd") { 
			s.handleCmd(w, r)
			return 
		}
		
		// e.g. GET /v1/locks/frontdoor
		if r.Method == http.MethodGet {
			id := strings.TrimPrefix(r.URL.Path, "/v1/locks/")
			id = strings.Trim(id, "/")
			
			user, _ := r.Context().Value(userContextKey).(string)
			if !s.canAccess(user, id) {
				http.Error(w, "access denied", http.StatusForbidden)
				return
			}
			
			s.mu.RLock()
			defer s.mu.RUnlock()
			stateRec, ok := s.state[id]
			if !ok {
				http.Error(w, "unknown lock", http.StatusNotFound)
				return
			}
			
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(stateRec)
			return
		}

		w.WriteHeader(http.StatusMethodNotAllowed)
	})

	mux.Handle("/v1/", s.auth(api))

	// Admin felület
	adminAPI := http.NewServeMux()
	adminAPI.HandleFunc("/v1/admin/gen-key", s.handleAdminGenKey)
	adminAPI.HandleFunc("/v1/admin/keys", s.handleAdminKeys)
	adminAPI.HandleFunc("/v1/admin/keys/", s.handleAdminDeleteKey)
	mux.Handle("/v1/admin/", s.adminAuth(adminAPI))
	mux.HandleFunc("/admin", s.handleAdminPage)
	mux.HandleFunc("/swagger", handleSwagger)
	mux.HandleFunc("/openapi.yaml", handleOpenAPISpec)

	tlsCfg := &tls.Config{
		GetCertificate: s.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	srv := &http.Server{
		Addr:      cfg.HTTP.Listen,
		Handler:   mux,
		TLSConfig: tlsCfg,
	}

	// Initial cert load
	if cfg.HTTP.CertFile != "" && cfg.HTTP.KeyFile != "" {
		if err := s.reloadCert(); err != nil {
			log.Fatalf("initial cert load failed: %v", err)
		}
	}

	// SIGHUP Reload
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGHUP)
		for range sig {
			log.Printf("SIGHUP received, reloading config and certificates...")
			newCfg, err := loadConfig(*cfgPath)
			if err != nil {
				log.Printf("reload failed: %v", err)
				continue
			}
			s.mu.Lock()
			s.cfg = newCfg
			s.mu.Unlock()

			if newCfg.HTTP.CertFile != "" && newCfg.HTTP.KeyFile != "" {
				if err := s.reloadCert(); err != nil {
					log.Printf("cert reload failed: %v", err)
				} else {
					log.Printf("certificates reloaded")
				}
			}
			log.Printf("config reloaded")
		}
	}()

	go func() {
		if cfg.HTTP.CertFile != "" && cfg.HTTP.KeyFile != "" {
			log.Printf("HTTPS listening on %s (dynamic reload enabled)", cfg.HTTP.Listen)
			if err := srv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("https server failed: %v", err)
			}
		} else {
			log.Printf("HTTP listening on %s (WARNING: plain HTTP)", cfg.HTTP.Listen)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("http server failed: %v", err)
			}
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down...")
	_ = srv.Shutdown(context.Background())
	if s.mc != nil { s.mc.Disconnect(250) }
}
