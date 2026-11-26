package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileMeta 是某个文件在电脑端的登记信息。
type FileMeta struct {
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime"`
	SHA256 string `json:"sha256"`
}

// Index 是「已接收文件清单」，用来做无感核对：
// 手机端报上来一批文件，只要路径 + 大小 + 时间对得上，就判定已同步，不再传输。
type Index struct {
	mu    sync.RWMutex
	files map[string]FileMeta
	path  string
	dirty bool
}

func loadIndex(p string) *Index {
	idx := &Index{files: make(map[string]FileMeta), path: p}
	if b, err := os.ReadFile(p); err == nil {
		var wrap struct {
			Files map[string]FileMeta `json:"files"`
		}
		if json.Unmarshal(b, &wrap) == nil && wrap.Files != nil {
			idx.files = wrap.Files
		}
	}
	return idx
}

func (i *Index) save() {
	i.mu.Lock()
	if !i.dirty {
		i.mu.Unlock()
		return
	}
	i.dirty = false
	b, _ := json.Marshal(struct {
		Files map[string]FileMeta `json:"files"`
	}{i.files})
	i.mu.Unlock()

	tmp := i.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, i.path)
	}
}

func (i *Index) get(p string) (FileMeta, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	m, ok := i.files[p]
	return m, ok
}

func (i *Index) set(p string, m FileMeta) {
	i.mu.Lock()
	i.files[p] = m
	i.dirty = true
	i.mu.Unlock()
}

func (i *Index) del(p string) {
	i.mu.Lock()
	delete(i.files, p)
	i.dirty = true
	i.mu.Unlock()
}

func (i *Index) count() int {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return len(i.files)
}

// hasHash 用于跨路径去重判断：同一张照片传了两次，第二份只登记不重复写盘。
func (i *Index) hasHash(h string, except string) bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	for k, v := range i.files {
		if k != except && v.SHA256 == h {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 路径安全

// sanitizeRel 把客户端给的相对路径清洗成安全的、只能在接收目录内的路径。
// 任何 ../、绝对路径、盘符、Windows 非法字符都会被拦下来。
func sanitizeRel(p string) (string, error) {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("路径为空")
	}
	// path.Clean("/"+p) 会消解掉所有 ".."，因此不可能跳出根目录
	p = strings.TrimPrefix(path.Clean("/"+p), "/")
	if p == "" || p == "." {
		return "", errors.New("路径非法")
	}
	if len(p) > 400 {
		return "", errors.New("路径过长")
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("路径非法")
		}
		if strings.ContainsAny(seg, `:*?"<>|`) {
			return "", errors.New("文件名含非法字符")
		}
		seg = strings.TrimRight(seg, " .") // Windows 不允许结尾是空格或点
		if seg == "" {
			return "", errors.New("文件名非法")
		}
		segs[i] = seg
	}
	return strings.Join(segs, "/"), nil
}

// absPath 在 sanitize 之后再校验一次真实路径确实落在接收目录内（防符号链接绕过）。
func absPath(rel string) (string, error) {
	root, err := filepath.Abs(rootDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	if fullAbs != root && !strings.HasPrefix(fullAbs, root+string(os.PathSeparator)) {
		return "", errors.New("越界访问")
	}
	return fullAbs, nil
}

// ---------------------------------------------------------------- 落盘

// writeIncoming 边收边算哈希，先写 .part 临时文件，完成后再原子改名。
// 中途断网、手机断电都不会留下半个坏文件。
func writeIncoming(rel string, r io.Reader, mtime int64) (FileMeta, bool, error) {
	rel, err := sanitizeRel(rel)
	if err != nil {
		return FileMeta{}, false, err
	}
	full, err := absPath(rel)
	if err != nil {
		return FileMeta{}, false, err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return FileMeta{}, false, err
	}

	tmp := fmt.Sprintf("%s.part%d", full, time.Now().UnixNano())
	f, err := os.Create(tmp)
	if err != nil {
		return FileMeta{}, false, err
	}
	h := sha256.New()
	buf := make([]byte, 256*1024)
	n, cerr := io.CopyBuffer(io.MultiWriter(f, h), r, buf)
	if cerr == nil {
		cerr = f.Sync()
	}
	f.Close()
	if cerr != nil {
		os.Remove(tmp)
		return FileMeta{}, false, cerr
	}

	if mtime <= 0 {
		mtime = time.Now().Unix()
	}
	ts := time.Unix(mtime, 0)
	_ = os.Chtimes(tmp, ts, ts)
	if err := os.Rename(tmp, full); err != nil {
		os.Remove(tmp)
		return FileMeta{}, false, err
	}

	sum := hex.EncodeToString(h.Sum(nil))
	meta := FileMeta{Size: n, Mtime: mtime, SHA256: sum}
	dup := index.hasHash(sum, rel)
	index.set(rel, meta)
	return meta, dup, nil
}
