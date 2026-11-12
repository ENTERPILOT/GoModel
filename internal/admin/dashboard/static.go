package dashboard

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

// compressibleTypes maps the text asset types worth gzipping to the
// Content-Type they are served with. Fonts and images are already compressed
// and are served as stored.
var compressibleTypes = map[string]string{
	".css":  "text/css; charset=utf-8",
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".json": "application/json",
	".map":  "application/json",
	".svg":  "image/svg+xml",
}

// staticAssets serves the embedded dashboard bundle. Text assets are gzipped
// once, on first request, and the compressed bytes are reused for every
// client that accepts gzip, so a cold dashboard load costs no compression CPU
// after the first one.
type staticAssets struct {
	files fs.FS
	raw   http.Handler
	gz    map[string]*gzipAsset
}

type gzipAsset struct {
	once        sync.Once
	contentType string
	data        []byte // nil when gzip does not shrink the file
}

func newStaticAssets(files fs.FS) (*staticAssets, error) {
	s := &staticAssets{
		files: files,
		raw:   http.FileServer(http.FS(files)),
		gz:    map[string]*gzipAsset{},
	}
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// http.FileServer redirects index.html requests to the directory;
		// leave them to it.
		if path.Base(name) == "index.html" {
			return nil
		}
		if contentType, ok := compressibleTypes[path.Ext(name)]; ok {
			s.gz[name] = &gzipAsset{contentType: contentType}
		}
		return nil
	})
	return s, err
}

// ServeHTTP serves a request whose path has the /admin/static/ prefix
// stripped. Range requests and clients without gzip get the stored bytes.
func (s *staticAssets) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	asset, ok := s.gz[name]
	if !ok {
		s.raw.ServeHTTP(w, r)
		return
	}
	header := w.Header()
	header.Set("Content-Type", asset.contentType)
	header.Add("Vary", "Accept-Encoding")
	if r.Header.Get("Range") != "" || !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		s.raw.ServeHTTP(w, r)
		return
	}
	data := asset.compressed(s.files, name)
	if data == nil {
		s.raw.ServeHTTP(w, r)
		return
	}
	// Embedded files carry no modification time or ETag, so there is no
	// conditional request to answer, as with the raw file server.
	header.Set("Content-Encoding", "gzip")
	header.Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func (a *gzipAsset) compressed(files fs.FS, name string) []byte {
	a.once.Do(func() {
		raw, err := fs.ReadFile(files, name)
		if err != nil {
			return
		}
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if _, err := zw.Write(raw); err != nil {
			return
		}
		if err := zw.Close(); err != nil || buf.Len() >= len(raw) {
			return
		}
		a.data = buf.Bytes()
	})
	return a.data
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip.
func acceptsGzip(header string) bool {
	for header != "" {
		var part string
		part, header, _ = strings.Cut(header, ",")
		coding, params, _ := strings.Cut(part, ";")
		coding = strings.TrimSpace(coding)
		if !strings.EqualFold(coding, "gzip") && !strings.EqualFold(coding, "x-gzip") {
			continue
		}
		q, ok := strings.CutPrefix(strings.TrimSpace(params), "q=")
		if !ok {
			return true
		}
		weight, err := strconv.ParseFloat(q, 64)
		return err != nil || weight > 0
	}
	return false
}
