package server

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/labstack/echo/v5"
)

// strangler sends every request v2 has no route for to the Vapor backend,
// untouched. It sits outside echo so a proxied request runs none of v2's
// middleware: Vapor adds its own CORS, security headers, request id and
// compression, and running both would duplicate headers and gzip twice.
//
// Ported routes can be sent back to Vapor without a deploy by listing them in
// V2_DISABLED_ROUTES as "METHOD /path/with/:params". V2_NATIVE_PREFIXES, when
// set, narrows v2 to the ported routes under those path prefixes.
type strangler struct {
	app      http.Handler
	legacy   http.Handler // nil once Vapor is gone: v2 answers everything
	probe    *echo.DefaultRouter
	probeMu  sync.RWMutex
	disabled map[string]struct{}
	prefixes []string // empty: every ported route is native
}

var errMiss = errors.New("no v2 route")

func newStrangler(legacyURL string, disabled, prefixes []string, logger *slog.Logger) (*strangler, error) {
	s := &strangler{disabled: map[string]struct{}{}}
	for _, p := range prefixes {
		if p = strings.TrimRight(strings.TrimSpace(p), "/"); p != "" {
			s.prefixes = append(s.prefixes, p)
		}
	}
	miss := func(*echo.Context) error { return errMiss }
	s.probe = echo.NewRouter(echo.RouterConfig{
		NotFoundHandler:         miss,
		MethodNotAllowedHandler: miss,
		// A preflight for a path v2 serves is answered by v2's CORS.
		OptionsMethodHandler: func(*echo.Context) error { return nil },
	})
	for _, d := range disabled {
		if d = strings.TrimSpace(d); d != "" {
			s.disabled[d] = struct{}{}
		}
	}
	if legacyURL != "" {
		target, err := url.Parse(legacyURL)
		if err != nil {
			return nil, err
		}
		proxy := &httputil.ReverseProxy{
			// Flush every write: AI chat answers stream.
			FlushInterval: -1,
			Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(target)
				// Keep the caller's chain and append our peer: Vapor's
				// ClientIP walks X-Forwarded-For from the right and skips
				// in-cluster hops, which this pod is. Proto and host come from
				// Traefik unchanged; SetXForwarded would rewrite them to the
				// plain-HTTP hop between the two pods.
				r.Out.Header["X-Forwarded-For"] = append(r.In.Header.Values("X-Forwarded-For"), clientHost(r.In))
				for _, h := range []string{"X-Forwarded-Proto", "X-Forwarded-Host"} {
					if v := r.In.Header.Values(h); len(v) > 0 {
						r.Out.Header[h] = v
					}
				}
				r.Out.Host = r.In.Host
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				logger.Error("legacy upstream failed", "path", r.URL.Path, "error", err)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":true,"code":"internal_server_error","reason":"Upstream unavailable"}`))
			},
		}
		s.legacy = proxy
	}
	return s, nil
}

func clientHost(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 && !strings.HasSuffix(host, "]") {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// mirror registers a route in the probe router; wired to echo's OnAddRoute.
func (s *strangler) mirror(route echo.Route) error {
	// Groups with middleware register catch-all not-found routes so their
	// middleware runs on a 404. Those are not routes v2 serves.
	if route.Method == echo.RouteNotFound {
		return nil
	}
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	_, err := s.probe.Add(echo.Route{
		Method:  route.Method,
		Path:    route.Path,
		Name:    route.Name,
		Handler: func(*echo.Context) error { return nil },
	})
	return err
}

// served reports whether v2 owns method+path.
func (s *strangler) served(r *http.Request) bool {
	// Not pooled: Context.Reset needs an *Echo, and the probe has none.
	c := echo.NewContext(r, discardWriter{})

	s.probeMu.RLock()
	h := s.probe.Route(c)
	s.probeMu.RUnlock()
	if h(c) != nil {
		return false
	}
	if len(s.disabled) > 0 {
		if _, off := s.disabled[r.Method+" "+c.Path()]; off {
			return false
		}
	}
	return s.native(c.Path())
}

// native reports whether route sits under one of the native prefixes, at a
// path segment boundary ("/v1/auth" covers "/v1/auth/login", not
// "/v1/authors").
func (s *strangler) native(route string) bool {
	if len(s.prefixes) == 0 {
		return true
	}
	for _, p := range s.prefixes {
		if route == p || strings.HasPrefix(route, p+"/") {
			return true
		}
	}
	return false
}

func (s *strangler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.legacy != nil && !s.served(r) {
		s.legacy.ServeHTTP(w, r)
		return
	}
	s.app.ServeHTTP(w, r)
}

// discardWriter backs the probe context, which never writes a response.
type discardWriter struct{}

func (discardWriter) Header() http.Header         { return http.Header{} }
func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardWriter) WriteHeader(int)             {}
