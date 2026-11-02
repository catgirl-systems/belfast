// Package admin is belfast's back-office: a JSON API under /api, behind a
// password, and the web app calling it (admin/, built into dist/ by `make
// admin`). It listens apart from the game server, on localhost by default.
package admin

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/ggmolly/belfast/internal/db"
	"github.com/ggmolly/belfast/internal/game"
	"github.com/ggmolly/belfast/internal/gateway"
	"github.com/ggmolly/belfast/internal/logger"
)

//go:embed all:dist
var dist embed.FS

// Server is the back-office of the game server Game runs.
type Server struct {
	Addr     string
	Password string
	Store    *db.Store
	Game     *game.Game
	// Gateway gives the clients' asset version, AssetDir keeps the art
	// fetched for it (see assets)
	Gateway  *gateway.Gateway
	AssetDir string
	// ListenAddr is the game server's -listen, DBPath the database's -db, as
	// the dashboard shows them
	ListenAddr string
	DBPath     string

	sessions sessions
	names    names
	assets   *assets
}

// Run loads the names of the game's things and serves the back-office until
// it fails.
func (server *Server) Run() error {
	if err := server.names.load(); err != nil {
		return err
	}
	server.watchLogsLive()
	server.assets = &assets{dir: server.AssetDir, gateway: server.Gateway, client: http.Client{Timeout: assetTimeout}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/login", server.handleLogin)
	mux.HandleFunc("GET /api/assets/{path...}", server.authedFile("Asset", server.handleAsset))
	server.registerLive2D(mux)
	server.registerPainting(mux)
	mux.HandleFunc("POST /api/logout", server.handleLogout)
	mux.HandleFunc("GET /api/live", server.handleLive)
	api := func(pattern string, handler func(*http.Request) (any, error)) {
		mux.HandleFunc(pattern, server.authed(handler))
	}
	// whether a password is needed, the web app skips its login when not
	api("GET /api/session", func(*http.Request) (any, error) {
		return map[string]bool{"password": server.Password != ""}, nil
	})
	api("GET /api/online", server.handleOnline)
	api("GET /api/commanders", server.handleCommanders)
	api("GET /api/commanders/{id}", server.handleCommander)
	api("GET /api/commanders/{id}/summary", server.handleSummary)
	api("POST /api/commanders/{id}/kick", server.handleKick)
	api("POST /api/commanders/{id}/mail", server.handleMail)
	api("POST /api/commanders/{id}/grant", server.handleGrant)
	api("POST /api/commanders/{id}/grant-all", server.handleGrantAll)
	api("POST /api/commanders/{id}/bags", server.handleBagMax)
	api("GET /api/catalog", server.handleCatalog)
	api("GET /api/catalog/ships", server.handleCatalogShips)
	api("GET /api/catalog/skins", server.handleCatalogSkins)
	api("GET /api/catalog/all", server.handleCatalogAll)
	api("GET /api/notices", server.handleNotices)
	api("POST /api/notices", server.handleAddNotice)
	api("DELETE /api/notices/{id}", server.handleDeleteNotice)
	server.registerAreas(api)
	mux.Handle("/", app())
	if server.Password == "" {
		logger.LogEvent("Admin", "Listen", "no password set (-admin-password, BELFAST_ADMIN_PASSWORD): anyone reaching "+server.Addr+" can use the back-office", logger.LOG_LEVEL_WARN)
	}
	logger.LogEvent("Admin", "Listen", "back-office on http://"+server.Addr, logger.LOG_LEVEL_INFO)
	return (&http.Server{Addr: server.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}).ListenAndServe()
}

// app serves the built web app, index.html for the paths it routes itself.
func app() http.Handler {
	files, _ := fs.Sub(dist, "dist")
	static := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(files, r.URL.Path[1:]); err == nil && r.URL.Path != "/" {
			static.ServeHTTP(w, r)
			return
		}
		index, err := fs.ReadFile(files, "index.html")
		if err != nil {
			http.Error(w, "the back-office isn't built: run make admin", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}

// httpError is an error with the status to answer it with.
type httpError struct {
	status int
	msg    string
}

func (err httpError) Error() string { return err.msg }

func badRequest(msg string) error { return httpError{http.StatusBadRequest, msg} }

func notFound(msg string) error { return httpError{http.StatusNotFound, msg} }

// authed answers a request with handler's value as JSON, or its error, once
// the session cookie is checked. Without a password every request is
// allowed: the back-office listens on localhost by default.
func (server *Server) authed(handler func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !server.loggedIn(w, r) {
			return
		}
		// a JSON body can't come from a cross-site form without a preflight
		if r.Method != http.MethodGet && r.ContentLength != 0 && r.Header.Get("Content-Type") != "application/json" {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "want application/json"})
			return
		}
		value, err := handler(r)
		if err == nil && r.Method != http.MethodGet {
			if event, ok := changed(r); ok {
				server.Game.Live.Publish(event)
			}
		}
		if err != nil {
			var known httpError
			if errors.As(err, &known) {
				writeJSON(w, known.status, map[string]string{"error": known.msg})
				return
			}
			if refused, ok := game.AdminRefusal(err); ok {
				status := http.StatusBadRequest
				if refused.Missing {
					status = http.StatusNotFound
				}
				writeJSON(w, status, map[string]string{"error": refused.Msg})
				return
			}
			logger.LogEvent("Admin", r.Method, r.URL.Path+": "+err.Error(), logger.LOG_LEVEL_ERROR)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, value)
	}
}

// loggedIn checks the session cookie, answering a 401 without it. Without a
// password every request is allowed.
func (server *Server) loggedIn(w http.ResponseWriter, r *http.Request) bool {
	if server.Password != "" && !server.sessions.valid(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not logged in"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}

// readJSON decodes the request body into value.
func readJSON(r *http.Request, value any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return badRequest("body: " + err.Error())
	}
	return nil
}

// pathUint is the request's {name} path value.
func pathUint(r *http.Request, name string) (uint32, error) {
	id, err := strconv.ParseUint(r.PathValue(name), 10, 32)
	if err != nil {
		return 0, badRequest(name + ": " + err.Error())
	}
	return uint32(id), nil
}

// pathID is the request's {id} path value.
func pathID(r *http.Request) (uint32, error) {
	return pathUint(r, "id")
}

// kicked answers a write of a commander: whether it was playing, and so
// kicked first.
func kicked(was bool, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return map[string]bool{"kicked": was}, nil
}

// commanderEdit answers a write of the {id} commander: its JSON body, when
// it has one, is read into a T for edit (see kicked).
func commanderEdit[T any](server *Server, r *http.Request, edit func(ctx context.Context, id uint32, body T) (bool, error)) (any, error) {
	commander, err := server.commander(r)
	if err != nil {
		return nil, err
	}
	var body T
	if r.ContentLength != 0 {
		if err := readJSON(r, &body); err != nil {
			return nil, err
		}
	}
	return kicked(edit(r.Context(), commander.ID, body))
}

// commanderAction is a write of the {id} commander without a body.
func (server *Server) commanderAction(edit func(ctx context.Context, id uint32) (bool, error)) func(*http.Request) (any, error) {
	return func(r *http.Request) (any, error) {
		return commanderEdit(server, r, func(ctx context.Context, id uint32, _ struct{}) (bool, error) { return edit(ctx, id) })
	}
}

// otherEdit is a write of the {id} commander about the {name} id of the
// path: a ship, a floor, another commander...
func (server *Server) otherEdit(name string, edit func(ctx context.Context, id, other uint32) (bool, error)) func(*http.Request) (any, error) {
	return func(r *http.Request) (any, error) {
		other, err := pathUint(r, name)
		if err != nil {
			return nil, err
		}
		return commanderEdit(server, r, func(ctx context.Context, id uint32, _ struct{}) (bool, error) { return edit(ctx, id, other) })
	}
}
