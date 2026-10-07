package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/stylishseahorse/rclone-backup-manager/web"
)

// Agent distribution: the dashboard serves the installer and the prebuilt
// agent binaries, so a device needs nothing but curl and the one-line command
// shown in the UI. Both are public: they contain no secrets.

var distName = regexp.MustCompile(`^wasabi-agent-linux-(amd64|arm64|armv7)(\.sha256)?$`)

func (s *Server) installScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(web.InstallScript)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !distName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	bin := filepath.Join(s.cfg.AgentDistDir, strings.TrimSuffix(name, ".sha256"))
	f, err := os.Open(bin)
	if err != nil {
		writeErr(w, http.StatusNotFound, "agent binary not available on this server for that architecture")
		return
	}
	defer f.Close()
	if strings.HasSuffix(name, ".sha256") {
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			writeErr(w, 500, "internal error")
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, hex.EncodeToString(h.Sum(nil))+"  "+filepath.Base(bin)+"\n")
		return
	}
	fi, _ := f.Stat()
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, filepath.Base(bin), fi.ModTime(), f)
}
