package main

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

const (
	appName = "LanSync"
	appVer  = "1.0.0"
)

//go:embed web/index.html
var indexHTML []byte

type Config struct {
	Port   int    `json:"port"`
	Dir    string `json:"dir"`
	Token  string `json:"token"`
	Notify bool   `json:"notify"`
}

var (
	cfg      Config
	cfgPath  string
	rootDir  string
	index    *Index
	listenAt string
	httpAddr string
)

// ---------------------------------------------------------------- 配置

func exeDir() string {
	e, err := os.Executable()
	if err != nil {
		d, _ := os.Getwd()
		return d
	}
	return filepath.Dir(e)
}

func newToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func loadConfig(dir string) {
	cfgPath = filepath.Join(dir, "config.json")
	home, _ := os.UserHomeDir()
	cfg = Config{
		Port:   8765,
		Dir:    filepath.Join(home, "Documents", appName),
		Token:  newToken(),
		Notify: true,
	}
	if b, err := os.ReadFile(cfgPath); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.Token == "" {
		cfg.Token = newToken()
	}
	if cfg.Port <= 0 {
		cfg.Port = 8765
	}
}

func saveConfig() {
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(cfgPath, b, 0o644)
}

// ---------------------------------------------------------------- 日志

func openLog(dir string) {
	f, err := os.OpenFile(filepath.Join(dir, "lansync.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err == nil {
		log.SetOutput(io.MultiWriter(f))
	}
	log.SetFlags(log.LstdFlags)
}

// ---------------------------------------------------------------- 通知（合并弹出）

var (
	notifyMu    sync.Mutex
	notifyCount int
	notifyBytes int64
	notifyTimer *time.Timer
)

func recordUpload(size int64) {
	notifyMu.Lock()
	notifyCount++
	notifyBytes += size
	if notifyTimer != nil {
		notifyTimer.Stop()
	}
	notifyTimer = time.AfterFunc(3*time.Second, flushNotify)
	notifyMu.Unlock()
}

func flushNotify() {
	notifyMu.Lock()
	n, b := notifyCount, notifyBytes
	notifyCount, notifyBytes = 0, 0
	notifyMu.Unlock()
	if n == 0 {
		return
	}
	title := "LanSync · 同步完成"
	body := fmt.Sprintf("已接收 %d 个文件，共 %s", n, humanBytes(b))
	log.Printf("[通知] %s %s", title, body)
	if cfg.Notify {
		toast(title, body)
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// toast 通过 PowerShell 调 WinRT 发系统通知，不引入额外依赖。
func toast(title, body string) {
	if runtime.GOOS != "windows" {
		return
	}
	esc := func(s string) string {
		s = strings.ReplaceAll(s, "&", "&amp;")
		s = strings.ReplaceAll(s, "<", "&lt;")
		s = strings.ReplaceAll(s, ">", "&gt;")
		s = strings.ReplaceAll(s, `"`, "&quot;")
		return strings.ReplaceAll(s, "'", "&apos;")
	}
	script := `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType=WindowsRuntime] > $null
[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType=WindowsRuntime] > $null
$xml = New-Object Windows.Data.Xml.Dom.XmlDocument
$xml.LoadXml('<toast><visual><binding template="ToastGeneric"><text>` + esc(title) + `</text><text>` + esc(body) + `</text></binding></visual></toast>')
$app = '{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe'
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier($app).Show([Windows.UI.Notifications.ToastNotification]::new($xml))`
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", script)
	_ = cmd.Run()
}

// ---------------------------------------------------------------- 工具

func lanIP() string {
	// 优先按网卡打分：真实局域网私有地址 > 虚拟网卡 > 链路本地
	virtual := []string{"vethernet", "vmware", "virtualbox", "hyper-v", "docker", "wsl", "loopback", "utun", "tap", "tun", "zt", "tailscale"}
	best, bestScore := "", 0
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(ifc.Name)
		isVirt := false
		for _, v := range virtual {
			if strings.Contains(name, v) {
				isVirt = true
				break
			}
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil {
				continue
			}
			ip := ipn.IP.To4()
			score := 0
			switch {
			case ip[0] == 192 && ip[1] == 168:
				score = 40
			case ip[0] == 10:
				score = 30
			case ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31:
				score = 20
			case ip[0] == 169 && ip[1] == 254:
				score = 1
			}
			if isVirt {
				score -= 15
			}
			if score > bestScore {
				best, bestScore = ip.String(), score
			}
		}
	}
	if best != "" {
		return best
	}
	// 兜底：拿默认路由出口地址
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil {
			return a.IP.String()
		}
	}
	return "127.0.0.1"
}

func pickPort(pref int) int {
	for p := pref; p < pref+30 && p < 65535; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", p))
		if err == nil {
			ln.Close()
			return p
		}
	}
	return pref
}

func isLocal(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// authorized 校验访问令牌：本机访问放行，局域网访问需带 X-Token / cookie / ?t=
func authorized(r *http.Request) bool {
	if isLocal(r) {
		return true
	}
	got := r.Header.Get("X-Token")
	if got == "" {
		got = r.URL.Query().Get("t")
	}
	if got == "" {
		if c, err := r.Cookie("lst"); err == nil {
			got = c.Value
		}
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(cfg.Token)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]interface{}{"ok": false, "error": msg})
}

// ---------------------------------------------------------------- 路由

type server struct{}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if e := recover(); e != nil {
			log.Printf("panic: %v", e)
		}
	}()

	p := r.URL.Path

	if p == "/" && r.Method == http.MethodGet {
		if !isLocal(r) && r.URL.Query().Get("t") == cfg.Token {
			http.SetCookie(w, &http.Cookie{
				Name: "lst", Value: cfg.Token, Path: "/",
				HttpOnly: true, MaxAge: 3600 * 24 * 365,
			})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
		return
	}

	if p == "/qr.png" {
		s.handleQR(w, r)
		return
	}

	if strings.HasPrefix(p, "/dav") {
		if !authorized(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="LanSync"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.handleDAV(w, r)
		return
	}

	if strings.HasPrefix(p, "/api/") {
		if !authorized(r) {
			writeErr(w, http.StatusUnauthorized, "未授权，请重新扫码配对")
			return
		}
		s.handleAPI(w, r)
		return
	}

	http.NotFound(w, r)
}

func (s *server) handleQR(w http.ResponseWriter, r *http.Request) {
	ip := lanIP()
	if v := r.URL.Query().Get("ip"); v != "" {
		ip = v
	}
	target := fmt.Sprintf("http://%s:%d/?t=%s", ip, cfg.Port, cfg.Token)
	png, err := qrcode.Encode(target, qrcode.Medium, 512)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *server) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/info":
		stats := scanStats()
		writeJSON(w, 200, map[string]interface{}{
			"ok":      true,
			"name":    appName,
			"ver":     appVer,
			"host":    lanIP(),
			"port":    cfg.Port,
			"url":     fmt.Sprintf("http://%s:%d", lanIP(), cfg.Port),
			"dav":     fmt.Sprintf("http://%s:%d/dav", lanIP(), cfg.Port),
			"token":   cfg.Token,
			"dir":     rootDir,
			"notify":  cfg.Notify,
			"count":   stats.Count,
			"bytes":   stats.Bytes,
			"last":    stats.Last,
			"version": runtime.Version(),
		})

	case r.URL.Path == "/api/check" && r.Method == http.MethodPost:
		s.handleCheck(w, r)

	case r.URL.Path == "/api/put" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		s.handlePut(w, r)

	case r.URL.Path == "/api/list":
		s.handleList(w, r)

	case r.URL.Path == "/api/delete" && r.Method == http.MethodPost:
		var body struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, 400, "参数错误")
			return
		}
		rel, err := sanitizeRel(body.Path)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		full, err := absPath(rel)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := os.Remove(full); err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		index.del(rel)
		writeJSON(w, 200, map[string]interface{}{"ok": true})

	case r.URL.Path == "/api/open" && r.Method == http.MethodPost:
		openFolder(rootDir)
		writeJSON(w, 200, map[string]interface{}{"ok": true})

	case r.URL.Path == "/api/quit" && r.Method == http.MethodPost:
		writeJSON(w, 200, map[string]interface{}{"ok": true})
		go func() {
			time.Sleep(300 * time.Millisecond)
			index.save()
			log.Println("收到退出指令，程序结束")
			os.Exit(0)
		}()

	case r.URL.Path == "/api/ping":
		writeJSON(w, 200, map[string]interface{}{"ok": true, "app": appName, "ver": appVer})

	default:
		writeErr(w, 404, "接口不存在")
	}
}

// handleCheck 是无感核对的核心：手机把本地清单报上来，电脑只回复「哪些需要传」。
func (s *server) handleCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			Path  string `json:"path"`
			Size  int64  `json:"size"`
			Mtime int64  `json:"mtime"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "参数错误")
		return
	}
	need := make([]string, 0, 32)
	have := 0
	for _, it := range body.Items {
		rel, err := sanitizeRel(it.Path)
		if err != nil {
			need = append(need, it.Path)
			continue
		}
		m, ok := index.get(rel)
		if ok && m.Size == it.Size && abs64(m.Mtime-it.Mtime) <= 5 {
			have++
			continue
		}
		need = append(need, rel)
	}
	log.Printf("核对清单：共 %d 项，已存在 %d，待传输 %d", len(body.Items), have, len(need))
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "need": need, "have": have, "total": len(body.Items),
	})
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (s *server) handlePut(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	if rel == "" {
		rel = r.Header.Get("X-Path")
	}
	if rel == "" {
		writeErr(w, 400, "缺少 path 参数")
		return
	}
	rel = strings.ReplaceAll(rel, "\\", "/")
	if !strings.Contains(rel, "/") {
		rel = time.Now().Format("2006-01-02") + "/" + rel // 浏览器兜底上传：自动按日期归档
	}

	if clean, e := sanitizeRel(rel); e == nil {
		rel = clean
	}

	mtime, _ := strconv.ParseInt(r.Header.Get("X-Mtime"), 10, 64)
	meta, dup, err := writeIncoming(rel, r.Body, mtime)
	if err != nil {
		log.Printf("接收失败 %s: %v", rel, err)
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("已接收 %s (%s)", rel, humanBytes(meta.Size))
	recordUpload(meta.Size)
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "path": rel, "size": meta.Size,
		"sha256": meta.SHA256, "dedup": dup,
	})
}

type fileRow struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
	SHA   string `json:"sha256"`
}

func (s *server) handleList(w http.ResponseWriter, r *http.Request) {
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows := make([]fileRow, 0, 256)
	_ = filepath.Walk(rootDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".part") || strings.Contains(filepath.Base(p), ".part") {
			return nil
		}
		rel, e := filepath.Rel(rootDir, p)
		if e != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		m, _ := index.get(rel)
		rows = append(rows, fileRow{Path: rel, Size: info.Size(), Mtime: info.ModTime().Unix(), SHA: m.SHA256})
		return nil
	})
	sort.Slice(rows, func(i, j int) bool { return rows[i].Mtime > rows[j].Mtime })
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	st := scanStats()
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "files": rows, "total": total,
		"count": st.Count, "bytes": st.Bytes, "last": st.Last,
	})
}

type stats struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
	Last  int64 `json:"last"`
}

func scanStats() stats {
	var st stats
	_ = filepath.Walk(rootDir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(p), ".part") {
			return nil
		}
		st.Count++
		st.Bytes += info.Size()
		if t := info.ModTime().Unix(); t > st.Last {
			st.Last = t
		}
		return nil
	})
	return st
}

// ensureAutostart 写一条 HKCU 启动项，开机后静默拉起（不需要管理员权限）。
func ensureAutostart() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	_ = exec.Command("reg", "add",
		`HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "LanSync", "/t", "REG_SZ", "/d", `"`+exe+`"`, "/f").Run()
}

func openFolder(dir string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", dir)
	case "darwin":
		cmd = exec.Command("open", dir)
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start()
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// ---------------------------------------------------------------- 启动

func main() {
	dir := exeDir()
	loadConfig(dir)
	openLog(dir)

	port := flag.Int("port", 0, "监听端口（默认 8765，被占用自动 +1）")
	dirFlag := flag.String("dir", "", "接收目录")
	noNotify := flag.Bool("no-notify", false, "关闭系统通知")
	noAuto := flag.Bool("no-autostart", false, "不写入开机自启项")
	openUI := flag.Bool("open", true, "启动后自动打开看板")
	testToast := flag.Bool("test-toast", false, "测试系统通知后退出")
	flag.Parse()

	if *port > 0 {
		cfg.Port = *port
	}
	if *dirFlag != "" {
		cfg.Dir = *dirFlag
	}
	if *noNotify {
		cfg.Notify = false
	}

	if *testToast {
		toast("LanSync · 通知测试", "如果你看到这条，说明系统通知正常")
		fmt.Println("已发送测试通知")
		return
	}

	rootDir = cfg.Dir
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		rootDir = filepath.Join(dir, "接收文件")
		_ = os.MkdirAll(rootDir, 0o755)
	}

	cfg.Port = pickPort(cfg.Port)
	saveConfig()

	if !*noAuto {
		ensureAutostart()
	}

	index = loadIndex(filepath.Join(dir, "index.json"))
	go func() {
		for {
			time.Sleep(3 * time.Second)
			index.save()
		}
	}()

	ip := lanIP()
	httpAddr = fmt.Sprintf("%s:%d", ip, cfg.Port)
	listenAt = fmt.Sprintf(":%d", cfg.Port)

	fmt.Println("──────────────────────────────────────────")
	fmt.Printf("  %s %s 已启动\n", appName, appVer)
	fmt.Printf("  接收目录 : %s\n", rootDir)
	fmt.Printf("  电脑看板 : http://127.0.0.1:%d\n", cfg.Port)
	fmt.Printf("  手机地址 : http://%s:%d\n", ip, cfg.Port)
	fmt.Printf("  配对令牌 : %s\n", cfg.Token)
	fmt.Println("──────────────────────────────────────────")

	log.Printf("启动完成 port=%d dir=%s", cfg.Port, rootDir)

	if *openUI && runtime.GOOS == "windows" {
		go func() {
			time.Sleep(600 * time.Millisecond)
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", cfg.Port))
		}()
	}

	srv := &http.Server{
		Addr:              listenAt,
		Handler:           &server{},
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		index.save()
		log.Println("程序退出")
		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("监听失败: %v", err)
	}
}
