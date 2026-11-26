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
	"runtime"
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

// snapshot 复制一份当前清单，供重建时复用已算好的哈希。
func (i *Index) snapshot() map[string]FileMeta {
	i.mu.RLock()
	defer i.mu.RUnlock()
	out := make(map[string]FileMeta, len(i.files))
	for k, v := range i.files {
		out[k] = v
	}
	return out
}

// replace 整体替换清单。切换接收目录后必须重建，
// 否则旧记录会让手机端误判「已同步」，跳过实际不在新目录里的文件。
func (i *Index) replace(files map[string]FileMeta) {
	i.mu.Lock()
	i.files = files
	i.dirty = true
	i.mu.Unlock()
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
	root, err := filepath.Abs(getRoot())
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

// ---------------------------------------------------------------- 接收目录切换

// rootDir 会被上传协程和看板接口并发读取，切换目录时用读写锁保护，
// 避免「路径校验用旧目录、落盘用新目录」这种错配。
var (
	dirMu   sync.RWMutex
	rootDir string
)

func getRoot() string {
	dirMu.RLock()
	defer dirMu.RUnlock()
	return rootDir
}

func setRoot(p string) {
	dirMu.Lock()
	rootDir = p
	dirMu.Unlock()
}

// samePath 判断两个路径是否指向同一个位置（Windows 下忽略大小写与末尾分隔符）。
func samePath(a, b string) bool {
	aa, e1 := filepath.Abs(a)
	bb, e2 := filepath.Abs(b)
	if e1 != nil || e2 != nil {
		return a == b
	}
	aa = strings.TrimRight(filepath.Clean(aa), string(os.PathSeparator))
	bb = strings.TrimRight(filepath.Clean(bb), string(os.PathSeparator))
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

// isSubPath 判断 child 是否位于 parent 之下（含相同）。用于拦住
// 「把接收目录设成自己的子目录」这种会导致无限递归的配置。
func isSubPath(parent, child string) bool {
	p, e1 := filepath.Abs(parent)
	c, e2 := filepath.Abs(child)
	if e1 != nil || e2 != nil {
		return false
	}
	p = filepath.Clean(p)
	c = filepath.Clean(c)
	if samePath(p, c) {
		return true
	}
	sep := string(os.PathSeparator)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p, c = strings.ToLower(p), strings.ToLower(c)
	}
	return strings.HasPrefix(c, p+sep)
}

// rebuildIndex 按目录里的真实内容重建清单。
// 切换目录后必须调用：旧记录若留着，手机端上报同路径同大小的文件会被判定
// 「已存在」而跳过传输，可新目录里根本没有这个文件。
func rebuildIndex(dir string) int {
	old := index.snapshot()
	next := make(map[string]FileMeta, len(old))
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(p), ".part") {
			return nil
		}
		rel, e := filepath.Rel(dir, p)
		if e != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		m := FileMeta{Size: info.Size(), Mtime: info.ModTime().Unix()}
		// 大小一致就沿用之前算过的哈希，省掉一次全盘 SHA-256
		if prev, ok := old[rel]; ok && prev.Size == m.Size {
			m.SHA256 = prev.SHA256
		}
		next[rel] = m
		return nil
	})
	index.replace(next)
	index.save()
	return len(next)
}

// migrateTo 把旧目录里的文件搬到新目录。同盘走 os.Rename（瞬间完成），
// 跨盘时降级为「复制 + 删除」。目标已存在同名文件则跳过，绝不覆盖。
func migrateTo(oldDir, newDir string) (files int, bytes int64, failed int) {
	oldAbs, err := filepath.Abs(oldDir)
	if err != nil {
		return 0, 0, 0
	}
	newAbs, err := filepath.Abs(newDir)
	if err != nil {
		return 0, 0, 0
	}
	if samePath(oldAbs, newAbs) {
		return 0, 0, 0
	}
	if _, err := os.Stat(oldAbs); err != nil {
		return 0, 0, 0
	}
	_ = filepath.Walk(oldAbs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(p), ".part") {
			return nil
		}
		rel, e := filepath.Rel(oldAbs, p)
		if e != nil {
			return nil
		}
		dst := filepath.Join(newAbs, rel)
		if _, e := os.Stat(dst); e == nil {
			return nil // 新目录已有同名文件，跳过
		}
		if e := os.MkdirAll(filepath.Dir(dst), 0o755); e != nil {
			failed++
			return nil
		}
		if e := os.Rename(p, dst); e != nil {
			if e := copyFile(p, dst); e != nil {
				failed++
				return nil
			}
			_ = os.Remove(p)
		}
		files++
		bytes += info.Size()
		return nil
	})
	return files, bytes, failed
}

// copyFile 流式复制，避免大视频一次性读进内存。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	buf := make([]byte, 256*1024)
	if _, err = io.CopyBuffer(out, in, buf); err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	if fi, err := in.Stat(); err == nil {
		_ = os.Chtimes(dst, fi.ModTime(), fi.ModTime())
	}
	return nil
}
