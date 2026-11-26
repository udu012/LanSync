package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------- 发件箱
//
// 手机不装 App 时，浏览器没法主动进电脑翻文件，只能由电脑这一侧把文件「摆出来」，
// 手机扫码打开一个网页再点下载。所以这个方向本质是「电脑推、手机拉」。
//
// 摆出来的方式有两种：
//   1. 系统文件选择框 → 只登记绝对路径，原地共享，不复制，发 20GB 也不占额外空间
//   2. 拖拽进看板   → 浏览器会把文件内容传上来一份，存在发件箱目录里
// 两种都登记在同一个清单里，24 小时后自动失效（副本会连文件一起删，原文件绝不动）。

const boxTTL = 24 * time.Hour

type sendItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime"`
	Added  int64  `json:"added"`
	Copied bool   `json:"copied"` // true=发件箱目录里的副本，过期要连文件一起删
}

var (
	boxMu    sync.Mutex
	boxFile  string
	boxDir   string
	boxItems []sendItem
)

// boxInit 在读配置之后调用：确定清单文件和副本目录，加载已有记录并起清理协程。
func boxInit(dir string) {
	boxFile = filepath.Join(dir, "outbox.json")
	boxDir = filepath.Join(dir, "outbox")
	_ = os.MkdirAll(boxDir, 0o755)

	if b, err := os.ReadFile(boxFile); err == nil {
		var wrap struct {
			Items []sendItem `json:"items"`
		}
		if json.Unmarshal(b, &wrap) == nil {
			boxItems = wrap.Items
		}
	}
	go func() {
		for {
			time.Sleep(time.Minute)
			boxClean()
		}
	}()
}

func boxSave() {
	boxMu.Lock()
	b, _ := json.Marshal(struct {
		Items []sendItem `json:"items"`
	}{boxItems})
	boxMu.Unlock()
	tmp := boxFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, boxFile)
	}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// boxAddPath 登记一个电脑上的现有文件（原地共享，不复制）。
func boxAddPath(p string) (sendItem, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return sendItem{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return sendItem{}, fmt.Errorf("找不到这个文件：%s", filepath.Base(p))
	}
	if st.IsDir() {
		return sendItem{}, fmt.Errorf("「%s」是文件夹，整目录发送还没做，请进去多选文件", st.Name())
	}

	boxMu.Lock()
	for _, it := range boxItems {
		if it.Path == abs && !it.Copied {
			boxMu.Unlock()
			return it, nil // 同一个文件重复点，不重复登记
		}
	}
	it := sendItem{
		ID:    newID(),
		Name:  st.Name(),
		Path:  abs,
		Size:  st.Size(),
		Mtime: st.ModTime().Unix(),
		Added: time.Now().Unix(),
	}
	boxItems = append([]sendItem{it}, boxItems...)
	boxMu.Unlock()
	boxSave()
	return it, nil
}

// boxAddStream 接收浏览器拖上来的文件，落成发件箱里的一份副本。
func boxAddStream(name string, r io.Reader) (sendItem, error) {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if clean, e := sanitizeRel(name); e == nil {
		name = clean
	} else {
		name = "文件"
	}
	if name == "" || name == "." {
		name = "文件"
	}
	if err := os.MkdirAll(boxDir, 0o755); err != nil {
		return sendItem{}, err
	}

	dst := filepath.Join(boxDir, name)
	if _, err := os.Stat(dst); err == nil {
		base := strings.TrimSuffix(name, filepath.Ext(name))
		ext := filepath.Ext(name)
		for i := 1; i < 1000; i++ {
			cand := fmt.Sprintf("%s (%d)%s", base, i, ext)
			if _, err := os.Stat(filepath.Join(boxDir, cand)); err != nil {
				dst = filepath.Join(boxDir, cand)
				name = cand
				break
			}
		}
	}

	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return sendItem{}, err
	}
	n, cerr := io.CopyBuffer(f, r, make([]byte, 256*1024))
	if cerr == nil {
		cerr = f.Sync()
	}
	f.Close()
	if cerr != nil {
		os.Remove(tmp)
		return sendItem{}, cerr
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return sendItem{}, err
	}

	st, _ := os.Stat(dst)
	it := sendItem{
		ID:     newID(),
		Name:   name,
		Path:   dst,
		Size:   n,
		Added:  time.Now().Unix(),
		Copied: true,
	}
	if st != nil {
		it.Mtime = st.ModTime().Unix()
	}

	boxMu.Lock()
	boxItems = append([]sendItem{it}, boxItems...)
	boxMu.Unlock()
	boxSave()
	return it, nil
}

func boxList() []sendItem {
	boxMu.Lock()
	out := make([]sendItem, len(boxItems))
	copy(out, boxItems)
	boxMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Added > out[j].Added })
	return out
}

func boxGet(id string) (sendItem, bool) {
	boxMu.Lock()
	defer boxMu.Unlock()
	for _, it := range boxItems {
		if it.ID == id {
			return it, true
		}
	}
	return sendItem{}, false
}

func boxDel(id string) {
	boxMu.Lock()
	keep := make([]sendItem, 0, len(boxItems))
	var gone *sendItem
	for i := range boxItems {
		if boxItems[i].ID == id {
			gone = &boxItems[i]
			continue
		}
		keep = append(keep, boxItems[i])
	}
	boxItems = keep
	boxMu.Unlock()
	// 只有副本才删文件；原地共享的文件是用户的，碰都不能碰
	if gone != nil && gone.Copied {
		os.Remove(gone.Path)
	}
	boxSave()
}

func boxClear() {
	boxMu.Lock()
	old := boxItems
	boxItems = nil
	boxMu.Unlock()
	for _, it := range old {
		if it.Copied {
			os.Remove(it.Path)
		}
	}
	boxSave()
}

// boxClean 清掉超过 24 小时的条目。原地共享的只删记录，副本连文件一起删。
func boxClean() {
	cut := time.Now().Add(-boxTTL).Unix()
	boxMu.Lock()
	keep := make([]sendItem, 0, len(boxItems))
	var expired []sendItem
	for _, it := range boxItems {
		if it.Added < cut {
			expired = append(expired, it)
			continue
		}
		keep = append(keep, it)
	}
	changed := len(keep) != len(boxItems)
	boxItems = keep
	boxMu.Unlock()

	for _, it := range expired {
		if it.Copied {
			os.Remove(it.Path)
		}
	}
	if changed {
		boxSave()
	}
}

// ---------------------------------------------------------------- 接口

// handleSendList 看板和手机收件页都用它拿清单。
func (s *server) handleSendList(w http.ResponseWriter, r *http.Request) {
	boxClean()
	items := boxList()
	rows := make([]map[string]interface{}, 0, len(items))
	for _, it := range items {
		_, err := os.Stat(it.Path)
		rows = append(rows, map[string]interface{}{
			"id":      it.ID,
			"name":    it.Name,
			"size":    it.Size,
			"mtime":   it.Mtime,
			"copied":  it.Copied,
			"missing": err != nil, // 原文件被挪走/删掉了，手机上点下载会提示失效
			"url":     "/s/f?id=" + it.ID,
		})
	}
	link := fmt.Sprintf("http://%s:%d/s?t=%s", lanIP(), runPort, cfg.Token)
	writeJSON(w, 200, map[string]interface{}{
		"ok":    true,
		"items": rows,
		"count": len(rows),
		"url":   link,
		"qr":    "/qr.png?u=" + url.QueryEscape(link),
		"ttl":   int(boxTTL / time.Second),
	})
}

// handleSendPick 弹系统的多选文件框，把选中的文件登记进发件箱。
func (s *server) handleSendPick(w http.ResponseWriter, r *http.Request) {
	paths, err := pickFiles()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"ok": false, "unsupported": true, "error": err.Error(),
		})
		return
	}
	added := make([]string, 0, len(paths))
	failed := make([]string, 0, len(paths))
	for _, p := range paths {
		if it, e := boxAddPath(p); e == nil {
			added = append(added, it.Name)
		} else {
			failed = append(failed, e.Error())
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"ok": true, "added": added, "failed": failed, "count": len(added),
	})
}

// handleSendPut 接收看板上拖拽进来的文件（会存一份副本）。
func (s *server) handleSendPut(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = r.Header.Get("X-Name")
	}
	if name == "" {
		writeErr(w, 400, "缺少文件名")
		return
	}
	it, err := boxAddStream(name, r.Body)
	if err != nil {
		log.Printf("发件箱写入失败 %s: %v", name, err)
		writeErr(w, 500, err.Error())
		return
	}
	log.Printf("发件箱新增 %s (%s)", it.Name, humanBytes(it.Size))
	writeJSON(w, 200, map[string]interface{}{"ok": true, "id": it.ID, "name": it.Name, "size": it.Size})
}

// handleSendDel 从发件箱移除一项（副本会连文件一起删，原文件不动）。
func (s *server) handleSendDel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "参数错误")
		return
	}
	boxDel(body.ID)
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func (s *server) handleSendClear(w http.ResponseWriter, r *http.Request) {
	boxClear()
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// handleSendGet 把文件流式发给手机。用 http.ServeContent 免费获得：
// Range 断点续传、Content-Length（浏览器才有进度条）、If-Modified-Since。
// 全程只读一小块就写出去，传 20GB 内存也不涨。
func (s *server) handleSendGet(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	it, ok := boxGet(id)
	if !ok {
		http.Error(w, "这个链接已经失效了，回电脑上重新发一次", http.StatusNotFound)
		return
	}
	f, err := os.Open(it.Path)
	if err != nil {
		http.Error(w, "文件已经不在电脑上了（可能被移动或删除）", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.Error(w, "文件读不了", http.StatusNotFound)
		return
	}

	// 中文文件名：ASCII 兜底给老浏览器，RFC 5987 的 filename* 给现代浏览器
	fallback := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' {
			return '_'
		}
		return r
	}, it.Name)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s", fallback, url.PathEscape(it.Name)))
	w.Header().Set("Content-Type", "application/octet-stream")
	log.Printf("手机下载 %s (%s)", it.Name, humanBytes(it.Size))
	http.ServeContent(w, r, it.Name, st.ModTime(), f)
}

// handleSendPage 手机扫开发件箱链接看到的页面。
func (s *server) handleSendPage(w http.ResponseWriter, r *http.Request) {
	// 扫码链接带 ?t=，顺手种个 Cookie，后面下载请求就不用再拼令牌了
	if !isLocal(r) && r.URL.Query().Get("t") == cfg.Token {
		http.SetCookie(w, &http.Cookie{
			Name: "lst", Value: cfg.Token, Path: "/",
			HttpOnly: true, MaxAge: 3600 * 24 * 365,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(sendHTML)
}
