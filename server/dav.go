package main

import (
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleDAV 提供一个够用的 WebDAV 子集，作为「不装我们的 App 也能自动同步」的兜底：
// 手机端用 FolderSync / 文件管理器指向 http://电脑IP:端口/dav 即可自动上传。
func (s *server) handleDAV(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/dav")
	rel := strings.Trim(strings.ReplaceAll(raw, "\\", "/"), "/")

	switch r.Method {
	case http.MethodOptions:
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("MS-Author-Via", "DAV")
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, COPY, MOVE, LOCK, UNLOCK")
		w.WriteHeader(http.StatusOK)

	case "PROPFIND":
		s.davPropfind(w, r, rel)

	case http.MethodGet, http.MethodHead:
		full, err := absPath(rel)
		if err != nil || rel == "" {
			http.Error(w, "not found", 404)
			return
		}
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			http.Error(w, "not found", 404)
			return
		}
		http.ServeFile(w, r, full)

	case http.MethodPut:
		if rel == "" {
			http.Error(w, "bad path", 400)
			return
		}
		mt, _ := time.Parse(http.TimeFormat, r.Header.Get("X-Update-Mtime"))
		mtime := mt.Unix()
		if mtime <= 0 {
			mtime = time.Now().Unix()
		}
		meta, _, err := writeIncoming(rel, r.Body, mtime)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		recordUpload(meta.Size)
		logDAV(r, "PUT", rel, meta.Size)
		w.Header().Set("ETag", `"`+meta.SHA256+`"`)
		w.WriteHeader(http.StatusCreated)

	case "MKCOL":
		full, err := absPath(rel)
		if err != nil {
			http.Error(w, "bad path", 409)
			return
		}
		if err := os.MkdirAll(full, 0o755); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		full, err := absPath(rel)
		if err != nil {
			http.Error(w, "bad path", 409)
			return
		}
		if err := os.RemoveAll(full); err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		index.del(rel)
		logDAV(r, "DELETE", rel, 0)
		w.WriteHeader(http.StatusNoContent)

	case "MOVE":
		dest := r.Header.Get("Destination")
		if dest == "" {
			http.Error(w, "no destination", 400)
			return
		}
		if u, err := url.Parse(dest); err == nil {
			dest = u.Path
		}
		destRel := strings.Trim(strings.TrimPrefix(dest, "/dav"), "/")
		src, e1 := absPath(rel)
		dst, e2 := absPath(destRel)
		if e1 != nil || e2 != nil {
			http.Error(w, "bad path", 409)
			return
		}
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		if err := os.Rename(src, dst); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		if m, ok := index.get(rel); ok {
			index.set(destRel, m)
			index.del(rel)
		}
		w.WriteHeader(http.StatusCreated)

	case "PROPPATCH":
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(207)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>`+
			escXML(davHref(rel))+`</D:href><D:propstat><D:prop/><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response></D:multistatus>`)

	case "LOCK":
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Lock-Token", "<opaquelocktoken:lansync>")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock>`+
			`<D:locktype><D:write/></D:locktype><D:lockscope><D:exclusive/></D:lockscope><D:depth>infinity</D:depth>`+
			`<D:timeout>Second-3600</D:timeout><D:locktoken><D:href>opaquelocktoken:lansync</D:href></D:locktoken>`+
			`</D:activelock></D:lockdiscovery></D:prop>`)

	case "UNLOCK":
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func logDAV(r *http.Request, op, rel string, size int64) {
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i > 0 {
		ip = ip[:i]
	}
	if size > 0 {
		log.Printf("WebDAV %s %s (%s) ← %s", op, rel, humanBytes(size), ip)
	} else {
		log.Printf("WebDAV %s %s ← %s", op, rel, ip)
	}
}

func davHref(rel string) string {
	if rel == "" {
		return "/dav/"
	}
	return (&url.URL{Path: "/dav/" + rel}).EscapedPath()
}

// davHrefDir 集合的 href 需要以 / 结尾，否则部分客户端识别不出目录
func davHrefDir(rel string) string {
	h := davHref(rel)
	if !strings.HasSuffix(h, "/") {
		h += "/"
	}
	return h
}

func escXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

func (s *server) davPropfind(w http.ResponseWriter, r *http.Request, rel string) {
	full, err := absPath(rel)
	if err != nil {
		http.Error(w, "bad path", 409)
		return
	}
	info, err := os.Stat(full)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}

	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<D:multistatus xmlns:D="DAV:">` + "\n")
	writeProp(&b, rel, info)

	if info.IsDir() && depth != "0" {
		entries, _ := os.ReadDir(full)
		for _, e := range entries {
			if strings.Contains(e.Name(), ".part") {
				continue
			}
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			ci, err := e.Info()
			if err != nil {
				continue
			}
			writeProp(&b, childRel, ci)
		}
	}
	b.WriteString(`</D:multistatus>`)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	_, _ = io.WriteString(w, b.String())
}

func writeProp(b *strings.Builder, rel string, info os.FileInfo) {
	name := filepath.Base(rel)
	if rel == "" {
		name = "dav"
	}
	href := davHref(rel)
	if info.IsDir() {
		href = davHrefDir(rel)
	}
	b.WriteString("<D:response><D:href>" + escXML(href) + "</D:href><D:propstat><D:prop>")
	b.WriteString("<D:displayname>" + escXML(name) + "</D:displayname>")
	b.WriteString("<D:getlastmodified>" + info.ModTime().UTC().Format(http.TimeFormat) + "</D:getlastmodified>")
	b.WriteString("<D:creationdate>" + info.ModTime().UTC().Format("2006-01-02T15:04:05Z") + "</D:creationdate>")
	if info.IsDir() {
		b.WriteString("<D:resourcetype><D:collection/></D:resourcetype>")
	} else {
		b.WriteString("<D:resourcetype/>")
		b.WriteString(fmt.Sprintf("<D:getcontentlength>%d</D:getcontentlength>", info.Size()))
		ct := mime.TypeByExtension(filepath.Ext(rel))
		if ct == "" {
			ct = "application/octet-stream"
		}
		b.WriteString("<D:getcontenttype>" + escXML(ct) + "</D:getcontenttype>")
	}
	b.WriteString(`<D:supportedlock><D:lockentry><D:lockscope><D:exclusive/></D:lockscope>` +
		`<D:locktype><D:write/></D:locktype></D:lockentry></D:supportedlock>`)
	b.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>\n")
}
