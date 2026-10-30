// Package ops ports the operator and documentation endpoints that Vapor
// mounted at the root: GET /openapi.yaml and GET /docs (routes.swift,
// registerOpenAPIDocsRoutes), GET /metrics (MetricsController, behind
// PROMETHEUS_ENABLED) and GET /internal/metrics (InternalMetricsController,
// guarded by METRICS_SECRET).
//
// Vapor mounted /docs, /openapi.yaml and /metrics only when their flag was
// on, so an off flag meant Vapor's route-miss 404. v2 always registers them
// and answers that same 404 while the flag is off.
package ops

import (
	"crypto/subtle"
	_ "embed"
	"net/http"
	"os"
	"strings"

	"norviq-api/internal/config"
	"norviq-api/internal/httpx"
	"norviq-api/internal/platform/metrics"
	"norviq-api/internal/server"
	"norviq-api/internal/store/opsdb"

	"github.com/labstack/echo/v5"
)

// openAPISpec is BundledOpenAPISpec: Sources/StockPlanBackend/openapi.yaml
// at the snapshot, verbatim.
//
//go:embed openapi.yaml
var openAPISpec string

// Feature mounts the four routes.
type Feature struct {
	// Metrics overrides metrics.Shared (tests).
	Metrics *metrics.Registry
}

// Name implements server.Feature.
func (Feature) Name() string { return "ops" }

// Register implements server.Feature.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	reg := f.Metrics
	if reg == nil {
		reg = metrics.Shared
	}
	// shouldExposeOpenAPIDocs: envBoolForHealth("API_DOCS_ENABLED",
	// default: environment != .production).
	docs := config.Bool(os.Getenv("API_DOCS_ENABLED"), !d.Config.IsProduction())
	prometheus := metrics.Enabled()
	q := opsdb.New(d.DB)

	gate := func(on bool) echo.MiddlewareFunc {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c *echo.Context) error {
				if !on {
					return httpx.NotFound("")
				}
				return next(c)
			}
		}
	}

	r.Root.GET("/openapi.yaml", func(c *echo.Context) error {
		return c.Blob(http.StatusOK, "application/yaml; charset=utf-8", []byte(openAPISpec))
	}, gate(docs))
	r.Root.GET("/docs", func(c *echo.Context) error {
		return c.Blob(http.StatusOK, "text/html; charset=utf-8", []byte(docsHTML))
	}, gate(docs))

	r.Root.GET("/metrics", func(c *echo.Context) error {
		return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(reg.Render()))
	}, gate(prometheus))

	r.Root.GET("/internal/metrics", func(c *echo.Context) error {
		// Read per request, as Environment.get was.
		secret := os.Getenv("METRICS_SECRET")
		if secret == "" {
			return httpx.Abort(http.StatusInternalServerError, "METRICS_SECRET is not configured")
		}
		presented := bearerToken(c.Request().Header.Get("Authorization"))
		// The length check leaks only the length; the compare is constant
		// time over equal lengths.
		if len(presented) != len(secret) || subtle.ConstantTimeCompare([]byte(presented), []byte(secret)) != 1 {
			return httpx.Abort(http.StatusUnauthorized, "")
		}
		n, err := q.CountUsers(c.Request().Context())
		if err != nil {
			return err
		}
		return httpx.OK(c, internalMetricsResponse{Users: n})
	})
	return nil
}

// internalMetricsResponse is InternalMetricsResponse.
type internalMetricsResponse struct {
	Users int64 `json:"users"`
}

// bearerToken is `req.headers.bearerAuthorization?.token ?? ""`: the
// "Bearer" scheme is matched case-insensitively.
func bearerToken(h string) string {
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}
	return token
}

// docsHTML is the Swagger UI page registerOpenAPIDocsRoutes served.
const docsHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>StockPlanBackend API Docs</title>
    <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  </head>
  <body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
    <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js"></script>
    <script>
      window.onload = () => {
        window.ui = SwaggerUIBundle({
          url: '/openapi.yaml',
          dom_id: '#swagger-ui',
          presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
          layout: 'BaseLayout'
        });
      };
    </script>
  </body>
</html>`
