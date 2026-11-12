package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// contentTypes maps the UI's file extensions to content types. It's fixed here,
// not read from the OS: Windows can map .js to text/plain in the registry, and
// nosniff would then stop the browser from running it.
var contentTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
}

// hashedAsset matches Vite's content-hashed file names ("index-BHAE32ir.js").
var hashedAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8}\.[a-z0-9]+$`)

type staticFile struct {
	body        []byte
	contentType string
	etag        string
	immutable   bool
}

// staticUI serves the embedded UI: files as they are, and index.html for any
// other path (SPA fallback). Missing files under /assets/ are a 404 instead, so
// a stale script tag gets an error rather than HTML.
type staticUI struct {
	files map[string]*staticFile // keyed by "/path"
	index *staticFile
	built bool
}

// notBuiltPage is served when the binary was built without the UI. It has no
// inline script or style, so it works under the CSP.
const notBuiltPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Woodshed</title></head>
<body>
<h1>Woodshed is running</h1>
<p>This build doesn't include the web UI. Build it with <code>make web</code> (or <code>npm run build</code> in <code>web/</code>) and rebuild the binary.</p>
<p>The syslog listeners and the API at <code>/api/</code> work without it.</p>
</body>
</html>
`

// newStaticUI loads every file of fsys into memory (the UI is small and
// embedded already) with its content type and ETag.
func newStaticUI(fsys fs.FS) (*staticUI, error) {
	s := &staticUI{files: map[string]*staticFile{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := path.Base(p)
		if strings.HasPrefix(base, ".") { // .keep and other dotfiles
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		ext := strings.ToLower(path.Ext(p))
		ct, ok := contentTypes[ext]
		if !ok {
			ct = "application/octet-stream"
		}
		sum := sha256.Sum256(b)
		s.files["/"+p] = &staticFile{
			body:        b,
			contentType: ct,
			etag:        `"` + hex.EncodeToString(sum[:12]) + `"`,
			immutable:   strings.HasPrefix(p, "assets/") && hashedAsset.MatchString(base),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if idx, ok := s.files["/index.html"]; ok {
		s.index, s.built = idx, true
	} else {
		body := []byte(notBuiltPage)
		sum := sha256.Sum256(body)
		s.index = &staticFile{body: body, contentType: contentTypes[".html"], etag: `"` + hex.EncodeToString(sum[:12]) + `"`}
	}
	return s, nil
}

func (s *staticUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, r.Method+" is not allowed here.")
		return
	}
	p := path.Clean("/" + r.URL.Path)
	f := s.files[p]
	if f == nil {
		if strings.HasPrefix(p, "/assets/") {
			writeError(w, http.StatusNotFound, "No such file: "+p)
			return
		}
		f = s.index
	}
	h := w.Header()
	h.Set("Content-Type", f.contentType)
	h.Set("ETag", f.etag)
	if f.immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.body))
}
