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
	"regexp"
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
	appVer  = "1.3.1"
)

//go:embed web/index.html
var indexHTML []byte

//go:embed web/send.html
var sendHTML []byte

type Config struct {
	Port   int    `json:"port"`
	Dir    string `json:"dir"`
	Token  string `json:"token"`
	Notify bool   `json:"notify"`
}

var (
	cfg      Config
	cfgPath  string
	index    *Index
	listenAt string
	httpAddr string
	runPort  int // 本次运行实际使用的端口（可能因占用而避让，但不写回配置）
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
		// 同时写文件和控制台：控制台版窗口里能实时看日志，
		// GUI 版下 stdout 无效，写入错误被忽略，无副作用。
		log.SetOutput(io.MultiWriter(os.Stdout, f))
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

type ifaceIP struct {
	IP    string `json:"ip"`
	Iface string `json:"iface"`
	Note  string `json:"note"`
	Score int    `json:"-"`
}

var virtualIface = []string{"vethernet", "vmware", "virtualbox", "hyper-v", "docker", "wsl", "loopback", "utun", "tap", "tun", "zt", "tailscale"}

func isVirtualIface(name string) bool {
	n := strings.ToLower(name)
	for _, v := range virtualIface {
		if strings.Contains(n, v) {
			return true
		}
	}
	return false
}

// hotspotNet 识别手机热点常见网段，便于看板上标注出来。
// 安卓默认 192.168.43.x，部分机型 192.168.42.x；Windows 移动热点 192.168.137.x；
// iPhone 个人热点 172.20.10.x。
func hotspotNet(ip net.IP) bool {
	switch {
	case ip[0] == 192 && ip[1] == 168 && (ip[2] == 43 || ip[2] == 42 || ip[2] == 137):
		return true
	case ip[0] == 172 && ip[1] == 20 && ip[2] == 10:
		return true
	}
	return false
}

// candidateIPs 列出本机所有可用的局域网地址，按「更像手机能访问到」排序。
// 电脑同时连着路由器、手机热点、虚拟机网卡时，靠这个列表让用户自己选，
// 比程序猜要可靠。
func candidateIPs() []ifaceIP {
	out := make([]ifaceIP, 0, 4)
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil {
				continue
			}
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
			note := ""
			if isVirtualIface(ifc.Name) {
				score -= 15
				note = "虚拟网卡"
			} else if hotspotNet(ip) {
				score += 5
				note = "手机热点"
			}
			if score <= 0 {
				continue
			}
			out = append(out, ifaceIP{IP: ip.String(), Iface: ifc.Name, Note: note, Score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })

	// 有正常地址时，丢掉 169.254.x（链路本地，DHCP 没拿到地址时的临时地址），
	// 它对用户没有意义，列出来只会让人困惑。
	if len(out) > 1 {
		kept := out[:0]
		for _, x := range out {
			if strings.HasPrefix(x.IP, "169.254.") {
				continue
			}
			kept = append(kept, x)
		}
		if len(kept) > 0 {
			out = kept
		}
	}
	return out
}

func lanIP() string {
	if list := candidateIPs(); len(list) > 0 {
		return list[0].IP
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

// findRunning 探测本机是否已有 LanSync 在跑，返回它实际监听的端口。
// 有了它，第二次双击 exe 就不会再起一个进程，而是直接把看板打开。
//
// 扫描范围要往前也覆盖：旧版本会把避让后的端口写回配置，导致配置里是 8771、
// 而实例实际跑在 8770。只看前半段会漏掉这种遗留情况。
func findRunning(pref int) int {
	start := pref - 30
	if start < 1024 {
		start = 1024
	}
	end := pref + 30
	if end > 65535 {
		end = 65535
	}
	client := &http.Client{Timeout: 300 * time.Millisecond}
	for p := start; p < end; p++ {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/ping", p), nil)
		if err != nil {
			continue
		}
		if cfg.Token != "" {
			req.Header.Set("X-Token", cfg.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var v struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		if v.Name == appName {
			return p
		}
	}
	return 0
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

	// 手机扫「发给手机」的二维码会落到这里：一个只有下载按钮的极简页面
	if p == "/s" || p == "/s/" {
		if !authorized(r) {
			http.Error(w, "链接已失效，回电脑上重新扫一次二维码", http.StatusUnauthorized)
			return
		}
		s.handleSendPage(w, r)
		return
	}
	if p == "/s/f" {
		if !authorized(r) {
			http.Error(w, "链接已失效，回电脑上重新扫一次二维码", http.StatusUnauthorized)
			return
		}
		s.handleSendGet(w, r)
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

	// ping 不校验令牌：只返回名字和版本，供程序做单实例探测、供 App 判断
	// 电脑是否在线。不含任何敏感信息。
	if p == "/api/ping" {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "name": appName, "ver": appVer})
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
	target := fmt.Sprintf("http://%s:%d/?t=%s", ip, runPort, cfg.Token)
	// ?u= 可以指定任意链接（发件箱的收件页要用），只放行 http(s)，别被当成任意内容生成器
	if v := r.URL.Query().Get("u"); strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		target = v
	}
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
			"ips":     candidateIPs(),
			"port":    runPort,
			"url":     fmt.Sprintf("http://%s:%d", lanIP(), runPort),
			"dav":     fmt.Sprintf("http://%s:%d/dav", lanIP(), runPort),
			"token":   cfg.Token,
			"dir":     getRoot(),
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

	case r.URL.Path == "/api/sendlist":
		s.handleSendList(w, r)

	case r.URL.Path == "/api/sendpick" && r.Method == http.MethodPost:
		s.handleSendPick(w, r)

	case r.URL.Path == "/api/sendput" && (r.Method == http.MethodPut || r.Method == http.MethodPost):
		s.handleSendPut(w, r)

	case r.URL.Path == "/api/senddel" && r.Method == http.MethodPost:
		s.handleSendDel(w, r)

	case r.URL.Path == "/api/sendclear" && r.Method == http.MethodPost:
		s.handleSendClear(w, r)

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
		openFolder(getRoot())
		writeJSON(w, 200, map[string]interface{}{"ok": true})

	case r.URL.Path == "/api/pickdir" && r.Method == http.MethodPost:
		s.handlePickDir(w, r)

	case r.URL.Path == "/api/setdir" && r.Method == http.MethodPost:
		s.handleSetDir(w, r)

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

// legacyDayPath 匹配 v1.2 的老归档路径：2026-08-14/IMG_0001.jpg
// v1.3 改成照搬手机目录（DCIM/Camera/IMG_0001.jpg），老记录靠这个特征识别出来做认领。
var legacyDayPath = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}/[^/]+$`)

// legacyFile 是索引里的一条老记录，认领时要把它的哈希一起搬到新路径，
// 否则跨路径去重（dedup）会失效。
type legacyFile struct {
	path string
	meta FileMeta
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
	adopted := 0

	// 老版本按「日期/文件名」归档，新版本按手机原始目录。升级后同一张照片的
	// 目标路径变了，光比路径会判「缺失」→ 整批重传几 GB。所以先用「大小 + 修改
	// 时间完全相同」把老记录认领到新路径（磁盘上 rename，手机不传字节）。
	wanted := make(map[string]bool, len(body.Items))
	for _, it := range body.Items {
		if rel, err := sanitizeRel(it.Path); err == nil {
			wanted[rel] = true
		}
	}
	legacy := make(map[string][]legacyFile) // "size:mtime" → 老记录列表
	for p, m := range index.snapshot() {
		if !legacyDayPath.MatchString(p) || wanted[p] {
			continue // 只认老路径，且不能被本次清单本身引用，防止互相顶替
		}
		k := fmt.Sprintf("%d:%d", m.Size, m.Mtime)
		legacy[k] = append(legacy[k], legacyFile{path: p, meta: m})
	}

	for _, it := range body.Items {
		rel, err := sanitizeRel(it.Path)
		if err != nil {
			need = append(need, it.Path)
			continue
		}
		m, ok := index.get(rel)
		// 命中索引还不够：用户可能在资源管理器里直接删/挪了文件，那种情况下
		// 清单里的记录就成了「幽灵」，光看清单会一直回「已存在」，手机永远不补传。
		// 所以必须再问一次磁盘。（多花 N 次 stat，3 万张照片约零点几秒）
		if ok && m.Size == it.Size && abs64(m.Mtime-it.Mtime) <= 5 && fileOnDisk(rel, it.Size) {
			have++
			continue
		}
		if it.Size > 0 {
			k := fmt.Sprintf("%d:%d", it.Size, it.Mtime)
			claimed := false
			for i, old := range legacy[k] {
				if adoptPath(old.path, rel) {
					index.del(old.path)
					index.set(rel, FileMeta{Size: it.Size, Mtime: it.Mtime, SHA256: old.meta.SHA256})
					legacy[k] = append(legacy[k][:i], legacy[k][i+1:]...)
					claimed = true
					adopted++
					break
				}
			}
			if claimed {
				have++
				continue
			}
		}
		need = append(need, rel)
	}
	if adopted > 0 {
		log.Printf("归档路径变更：%d 个老文件已就地改名到新目录，无需手机重传", adopted)
	}
	log.Printf("核对清单：共 %d 项，已存在 %d，待传输 %d", len(body.Items), have, len(need))
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "need": need, "have": have, "total": len(body.Items),
	})
}

// fileOnDisk 校验索引里那条记录对应的文件是否真的还在、大小是否一致。
func fileOnDisk(rel string, size int64) bool {
	full, err := absPath(rel)
	if err != nil {
		return false
	}
	st, err := os.Stat(full)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Size() == size
}

// healIndex 启动后在后台跑一遍体检：把「磁盘上已经没有」的记录从清单里剔掉。
// 不这么做的话，历史遗留的幽灵记录会一直污染看板计数，也会让跨路径去重误判。
// 但如果缺失比例高得离谱（比如接收目录在没挂载的移动硬盘上），就只告警不删，
// 免得一次误判把整份清单清空。
func healIndex() {
	snap := index.snapshot()
	if len(snap) == 0 {
		return
	}
	missing := make([]string, 0, 8)
	for p := range snap {
		full, err := absPath(p)
		if err != nil {
			missing = append(missing, p)
			continue
		}
		if st, err := os.Stat(full); err != nil || st.IsDir() {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return
	}
	if len(snap) > 20 && float64(len(missing))/float64(len(snap)) > 0.5 {
		log.Printf("启动自检：清单 %d 条里有 %d 条找不到文件，比例过高，可能是磁盘没挂上，这次不动清单",
			len(snap), len(missing))
		return
	}
	for _, p := range missing {
		index.del(p)
	}
	index.save()
	log.Printf("启动自检：%d 条记录对应的文件已不在磁盘上，已从清单移除（下次同步会补传）", len(missing))
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

// handlePickDir 弹出系统「选择文件夹」对话框，只返回用户选的路径，不改变任何设置。
func (s *server) handlePickDir(w http.ResponseWriter, r *http.Request) {
	p, err := pickFolder(getRoot())
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"ok": false, "unsupported": true, "error": err.Error(),
		})
		return
	}
	if p == "" {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "cancelled": true})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "dir": p})
}

// handleSetDir 切换接收目录。move=true 时把旧目录里已有的文件一起搬过去。
func (s *server) handleSetDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Dir  string `json:"dir"`
		Move bool   `json:"move"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "参数错误")
		return
	}

	dir := os.ExpandEnv(strings.TrimSpace(body.Dir))
	if dir == "" {
		writeErr(w, 400, "目录不能为空")
		return
	}
	// 支持 ~ 手写简写
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimLeft(dir[1:], `/\`))
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		writeErr(w, 400, "路径无效")
		return
	}

	old := getRoot()
	if samePath(old, abs) {
		writeJSON(w, 200, map[string]interface{}{
			"ok": true, "dir": abs, "unchanged": true,
		})
		return
	}
	// 互相包含会让迁移和统计陷入递归，直接拒绝
	if isSubPath(abs, old) || isSubPath(old, abs) {
		writeErr(w, 400, "新目录不能和当前目录互相包含，请换一个位置（例如 D:\\手机备份）")
		return
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		writeErr(w, 400, "无法创建目录："+err.Error())
		return
	}
	probe := filepath.Join(abs, ".lansync-write-test")
	if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
		writeErr(w, 400, "这个位置没有写入权限，换一个试试")
		return
	}
	os.Remove(probe)

	// 先切目录、再搬文件：搬移可能要好几分钟，切换后新的上传直接落到新目录，
	// 不会出现「传到一半目标变了」，也不会把新文件留在旧目录里。
	setRoot(abs)
	cfg.Dir = abs
	saveConfig()

	moved, bytes, failed := 0, int64(0), 0
	if body.Move {
		moved, bytes, failed = migrateTo(old, abs)
	}
	n := rebuildIndex(abs)

	log.Printf("接收目录切换：%s → %s（搬移 %d 个 / %s，失败 %d，索引 %d 项）",
		old, abs, moved, humanBytes(bytes), failed, n)
	writeJSON(w, 200, map[string]interface{}{
		"ok":      true,
		"dir":     abs,
		"old":     old,
		"moved":   moved,
		"bytes":   bytes,
		"failed":  failed,
		"indexed": n,
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
	root := getRoot()
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".part") || strings.Contains(filepath.Base(p), ".part") {
			return nil
		}
		rel, e := filepath.Rel(root, p)
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
	_ = filepath.Walk(getRoot(), func(p string, info os.FileInfo, err error) error {
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
	force := flag.Bool("force", false, "即使已有实例在运行，也强制再启动一个")
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

	// 单实例：已经有一个在跑，就只把看板打开，不再起新进程。
	// 这解决了「双击一次多一个进程、端口一路上涨」的问题。
	if !*force {
		if running := findRunning(cfg.Port); running > 0 {
			fmt.Printf("LanSync 已经在运行（端口 %d），正在打开看板…\n", running)
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", running))
			return
		}
	}

	setRoot(cfg.Dir)
	if err := os.MkdirAll(getRoot(), 0o755); err != nil {
		setRoot(filepath.Join(dir, "接收文件"))
		_ = os.MkdirAll(getRoot(), 0o755)
	}

	// 端口避让只影响本次运行，结果不写回配置文件。
	// 否则 8765 被占用一次后，配置里就变成 8766，以后每次启动都从 8766 起，越开越大。
	runPort = pickPort(cfg.Port)
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

	boxInit(dir)
	go healIndex()

	ip := lanIP()
	httpAddr = fmt.Sprintf("%s:%d", ip, runPort)
	listenAt = fmt.Sprintf(":%d", runPort)

	fmt.Println("──────────────────────────────────────────")
	fmt.Printf("  %s %s 已启动\n", appName, appVer)
	fmt.Printf("  接收目录 : %s\n", getRoot())
	fmt.Printf("  电脑看板 : http://127.0.0.1:%d\n", runPort)
	fmt.Printf("  手机地址 : http://%s:%d\n", ip, runPort)
	fmt.Printf("  配对令牌 : %s\n", cfg.Token)
	fmt.Println("──────────────────────────────────────────")

	log.Printf("启动完成 port=%d dir=%s", runPort, getRoot())

	if *openUI && runtime.GOOS == "windows" {
		go func() {
			time.Sleep(600 * time.Millisecond)
			openBrowser(fmt.Sprintf("http://127.0.0.1:%d", runPort))
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
