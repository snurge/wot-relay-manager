package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nhooyr.io/websocket"
)

//go:embed static/*
var staticFS embed.FS

type appConfig struct {
	BindAddr     string
	RelayHTTP    string
	RelayWS      string
	RelayEnv     string
	ServiceName  string
	Username     string
	PasswordHash string
}

type server struct {
	cfg      appConfig
	sessions map[string]time.Time
	mu       sync.Mutex
}

func main() {
	cfg := appConfig{
		BindAddr:     env("BIND_ADDR", "127.0.0.1:4781"),
		RelayHTTP:    env("RELAY_HTTP", "http://127.0.0.1:3334"),
		RelayWS:      env("RELAY_WS", "ws://127.0.0.1:3334"),
		RelayEnv:     env("RELAY_ENV", "/etc/systemd/system/wot-relay.env"),
		ServiceName:  env("SERVICE_NAME", "wot-relay.service"),
		Username:     env("MANAGER_USERNAME", "relayadmin"),
		PasswordHash: os.Getenv("MANAGER_PASSWORD_SHA256"),
	}
	if cfg.PasswordHash == "" {
		log.Fatal("MANAGER_PASSWORD_SHA256 is required")
	}

	s := &server{cfg: cfg, sessions: map[string]time.Time{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/static/", s.static)
	mux.HandleFunc("/api/login", s.login)
	mux.HandleFunc("/api/logout", s.auth(s.logout))
	mux.HandleFunc("/api/overview", s.auth(s.overview))
	mux.HandleFunc("/api/config", s.auth(s.config))
	mux.HandleFunc("/api/restart", s.auth(s.restart))
	mux.HandleFunc("/api/notes", s.auth(s.notes))
	mux.HandleFunc("/api/feed", s.auth(s.feed))
	mux.HandleFunc("/api/logs", s.auth(s.logs))

	log.Printf("wot-relay-manager listening on http://%s", cfg.BindAddr)
	log.Fatal(http.ListenAndServe(cfg.BindAddr, securityHeaders(mux)))
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, _ := staticFS.ReadFile("static/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *server) static(w http.ResponseWriter, r *http.Request) {
	http.FileServer(http.FS(staticFS)).ServeHTTP(w, r)
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct{ Username, Password string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(in.Password))
	okUser := subtle.ConstantTimeCompare([]byte(in.Username), []byte(s.cfg.Username)) == 1
	okPass := subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(strings.ToLower(s.cfg.PasswordHash))) == 1
	if !okUser || !okPass {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	token, err := randomToken()
	if err != nil {
		http.Error(w, "session error", http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(12 * time.Hour)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "wotmgr", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 43200})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	c, _ := r.Cookie("wotmgr")
	if c != nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "wotmgr", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) auth(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("wotmgr")
		if err != nil || c.Value == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		expires, ok := s.sessions[c.Value]
		if ok && time.Now().After(expires) {
			delete(s.sessions, c.Value)
			ok = false
		}
		s.mu.Unlock()
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fn(w, r)
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	statsText, _ := httpGet(s.cfg.RelayHTTP + "/debug/stats")
	stats := parseDebugStats(statsText)
	cfg, _ := readEnvFile(s.cfg.RelayEnv)
	status := systemStatus(s.cfg.ServiceName)
	perf := journalPerformance(s.cfg.ServiceName, 90)
	disk := diskUsage(cfg["DB_PATH"])
	writeJSON(w, map[string]any{
		"stats": stats, "status": status, "performance": perf,
		"config": cfg, "disk": disk, "relay_http": s.cfg.RelayHTTP,
		"relay_ws": s.cfg.RelayWS, "relay_env": s.cfg.RelayEnv,
	})
}

func (s *server) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg, err := readEnvFile(s.cfg.RelayEnv)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, cfg)
	case http.MethodPost:
		var in map[string]string
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := updateEnvFile(s.cfg.RelayEnv, in); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "restart_required": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) restart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := exec.Command("systemctl", "restart", s.cfg.ServiceName).CombinedOutput()
	if err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) logs(w http.ResponseWriter, r *http.Request) {
	out, err := exec.Command("journalctl", "-u", s.cfg.ServiceName, "-n", "120", "--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"logs": string(out)})
}

func (s *server) notes(w http.ResponseWriter, r *http.Request) {
	limit := clampInt(queryInt(r, "limit", 25), 1, 100)
	kind := r.URL.Query().Get("kind")
	author := strings.TrimSpace(r.URL.Query().Get("author"))
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	filter := map[string]any{"limit": limit}
	if kind != "" {
		if n, err := strconv.Atoi(kind); err == nil {
			filter["kinds"] = []int{n}
		}
	}
	if author != "" {
		hexAuthor, err := normalizePubkey(author)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filter["authors"] = []string{hexAuthor}
	}
	events, err := queryRelay(s.cfg.RelayWS, filter, 6*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if search != "" {
		needle := strings.ToLower(search)
		filtered := events[:0]
		for _, ev := range events {
			if strings.Contains(strings.ToLower(fmt.Sprint(ev["content"])), needle) {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	writeJSON(w, map[string]any{"events": events})
}

func (s *server) feed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	kind := queryInt(r, "kind", 1)
	filter := map[string]any{
		"kinds": []int{kind},
		"since": time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	writeSSE(w, "status", map[string]string{"status": "connecting"})
	flusher.Flush()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c, _, err := websocket.Dial(ctx, s.cfg.RelayWS, nil)
	if err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "done")

	sub := "feed-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	msg, _ := json.Marshal([]any{"REQ", sub, filter})
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		writeSSE(w, "error", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	writeSSE(w, "status", map[string]string{"status": "listening"})
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		default:
			readCtx, cancelRead := context.WithTimeout(ctx, 2*time.Second)
			_, data, err := c.Read(readCtx)
			cancelRead()
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 3 {
				continue
			}
			var typ string
			json.Unmarshal(frame[0], &typ)
			if typ != "EVENT" {
				continue
			}
			var ev map[string]any
			if json.Unmarshal(frame[2], &ev) == nil {
				writeSSE(w, "note", ev)
				flusher.Flush()
			}
		}
	}
}

func queryRelay(relayURL string, filter map[string]any, timeout time.Duration) ([]map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	u, err := url.Parse(relayURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		return nil, errors.New("invalid RELAY_WS")
	}
	c, _, err := websocket.Dial(ctx, relayURL, nil)
	if err != nil {
		return nil, err
	}
	defer c.Close(websocket.StatusNormalClosure, "done")
	sub := "mgr-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	req := []any{"REQ", sub, filter}
	msg, _ := json.Marshal(req)
	if err := c.Write(ctx, websocket.MessageText, msg); err != nil {
		return nil, err
	}
	events := []map[string]any{}
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			if len(events) > 0 {
				return events, nil
			}
			return nil, err
		}
		var frame []json.RawMessage
		if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
			continue
		}
		var typ string
		json.Unmarshal(frame[0], &typ)
		if typ == "EOSE" {
			return events, nil
		}
		if typ == "EVENT" && len(frame) >= 3 {
			var ev map[string]any
			if json.Unmarshal(frame[2], &ev) == nil {
				events = append(events, ev)
			}
		}
	}
}

func normalizePubkey(input string) (string, error) {
	input = strings.TrimSpace(strings.ToLower(input))
	if input == "" {
		return "", errors.New("empty pubkey")
	}
	if strings.HasPrefix(input, "npub1") {
		return npubToHex(input)
	}
	if len(input) == 64 {
		if _, err := hex.DecodeString(input); err == nil {
			return input, nil
		}
	}
	return "", errors.New("author must be npub1... or a 64-character hex pubkey")
}

func npubToHex(npub string) (string, error) {
	hrp, data, err := bech32Decode(npub)
	if err != nil {
		return "", err
	}
	if hrp != "npub" {
		return "", errors.New("author must use npub prefix")
	}
	decoded, err := convertBits(data, 5, 8, false)
	if err != nil {
		return "", err
	}
	if len(decoded) != 32 {
		return "", errors.New("npub did not decode to a 32-byte pubkey")
	}
	return hex.EncodeToString(decoded), nil
}

func bech32Decode(s string) (string, []byte, error) {
	const charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"
	if strings.ToLower(s) != s {
		return "", nil, errors.New("npub must be lowercase")
	}
	pos := strings.LastIndexByte(s, '1')
	if pos < 1 || pos+7 > len(s) {
		return "", nil, errors.New("invalid npub format")
	}
	hrp := s[:pos]
	chars := s[pos+1:]
	values := make([]byte, len(chars))
	for i, r := range chars {
		idx := strings.IndexRune(charset, r)
		if idx < 0 {
			return "", nil, errors.New("invalid npub character")
		}
		values[i] = byte(idx)
	}
	if !bech32VerifyChecksum(hrp, values) {
		return "", nil, errors.New("invalid npub checksum")
	}
	return hrp, values[:len(values)-6], nil
}

func bech32VerifyChecksum(hrp string, data []byte) bool {
	values := append(bech32HrpExpand(hrp), data...)
	return bech32Polymod(values) == 1
}

func bech32HrpExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for _, r := range hrp {
		out = append(out, byte(r>>5))
	}
	out = append(out, 0)
	for _, r := range hrp {
		out = append(out, byte(r&31))
	}
	return out
}

func bech32Polymod(values []byte) uint32 {
	generator := []uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= generator[i]
			}
		}
	}
	return chk
}

func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, error) {
	acc := uint(0)
	bits := uint(0)
	maxv := uint((1 << toBits) - 1)
	maxAcc := uint((1 << (fromBits + toBits - 1)) - 1)
	out := []byte{}
	for _, value := range data {
		v := uint(value)
		if v>>fromBits != 0 {
			return nil, errors.New("invalid bech32 data range")
		}
		acc = ((acc << fromBits) | v) & maxAcc
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			out = append(out, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, errors.New("invalid npub padding")
	}
	return out, nil
}

func httpGet(target string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(target)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return string(b), err
}

func parseDebugStats(text string) map[string]any {
	out := map[string]any{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "Debug Statistics:" {
			continue
		}
		if strings.HasSuffix(line, ":") {
			section = strings.TrimSuffix(line, ":")
			out[section] = map[string]int64{}
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 || section == "" {
			continue
		}
		n := firstInt(parts[1])
		out[section].(map[string]int64)[parts[0]] = n
	}
	return out
}

func systemStatus(service string) map[string]string {
	props := []string{"ActiveState", "SubState", "MainPID", "NTasks", "MemoryCurrent", "CPUUsageNSec", "ExecMainStartTimestamp", "LoadState", "UnitFileState"}
	args := append([]string{"show", service}, prefixed("-p", props)...)
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	m := map[string]string{"error": ""}
	if err != nil {
		m["error"] = string(out)
		return m
	}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

func prefixed(flag string, vals []string) []string {
	out := []string{}
	for _, v := range vals {
		out = append(out, flag, v)
	}
	return out
}

func journalPerformance(service string, lines int) []map[string]any {
	out, err := exec.Command("journalctl", "-u", service, "-n", strconv.Itoa(lines), "--no-pager", "-o", "short-iso").CombinedOutput()
	if err != nil {
		return nil
	}
	re := regexp.MustCompile(`([0-9T:\-+]+).*Performance: Events/min=(\d+), Rejected/min=(\d+), Archived/min=(\d+), GC/min=(\d+), Goroutines=(\d+)`)
	points := []map[string]any{}
	for _, line := range strings.Split(string(out), "\n") {
		m := re.FindStringSubmatch(line)
		if len(m) != 7 {
			continue
		}
		points = append(points, map[string]any{
			"time": m[1], "events": atoi(m[2]), "rejected": atoi(m[3]),
			"archived": atoi(m[4]), "gc": atoi(m[5]), "goroutines": atoi(m[6]),
		})
	}
	return points
}

func diskUsage(path string) map[string]any {
	if path == "" {
		return map[string]any{}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return map[string]any{"error": err.Error(), "path": path}
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return map[string]any{"path": path, "total": total, "free": free, "used": total - free}
}

func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		out[strings.TrimSpace(k)] = unquoteEnv(strings.TrimSpace(v))
	}
	return out, sc.Err()
}

func updateEnvFile(path string, updates map[string]string) error {
	allowed := map[string]bool{
		"RELAY_NAME": true, "RELAY_DESCRIPTION": true, "RELAY_URL": true, "RELAY_ICON": true,
		"RELAY_CONTACT": true, "REFRESH_INTERVAL_HOURS": true, "MINIMUM_FOLLOWERS": true,
		"ARCHIVAL_SYNC": true, "ARCHIVE_REACTIONS": true, "MAX_AGE_DAYS": true,
		"IGNORE_FOLLOWS_LIST": true, "SEED_RELAYS": true, "ARCHIVE_KINDS": true,
		"PUBLIC_WRITE_RELAY": true, "WRITE_ALLOWLIST": true,
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(current), "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || !strings.Contains(trim, "=") {
			continue
		}
		k, _, _ := strings.Cut(trim, "=")
		k = strings.TrimSpace(k)
		if !allowed[k] {
			continue
		}
		if v, ok := updates[k]; ok {
			lines[i] = k + "=" + quoteEnv(v)
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(updates))
	for k := range updates {
		if allowed[k] && !seen[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+"="+quoteEnv(updates[k]))
	}
	backup := path + ".bak." + time.Now().Format("20060102150405")
	if err := os.WriteFile(backup, current, 0600); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func unquoteEnv(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		return v[1 : len(v)-1]
	}
	return v
}

func quoteEnv(v string) string {
	return strconv.Quote(v)
}

func firstInt(s string) int64 {
	re := regexp.MustCompile(`\d+`)
	m := re.FindString(s)
	if m == "" {
		return 0
	}
	n, _ := strconv.ParseInt(m, 10, 64)
	return n
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func queryInt(r *http.Request, key string, fallback int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return n
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeSSE(w io.Writer, event string, v any) {
	data, _ := json.Marshal(v)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}
