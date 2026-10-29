// Package reporting ports Vapor's AdvancedReportingController
// (/v1/reporting/*), AdvancedReportWorker and AdvancedReportRetentionJob:
// report templates, schedules and runs, PDF rendering through Gotenberg,
// the stored-zip XLSX writer, local artifact storage and signed download
// links.
package reporting

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"norviq-api/internal/auth"
	"norviq-api/internal/platform/entitlements"
	"norviq-api/internal/server"
	"norviq-api/internal/store/reportingdb"

	"github.com/labstack/echo/v5"
)

// devSigningSecret is configure.swift's fallback outside production.
const devSigningSecret = "development-report-signing-secret-change-me"

// Settings are the variables configure.swift read for reporting.
type Settings struct {
	// StoragePath is ADVANCED_REPORT_STORAGE_PATH, defaulting to
	// <working directory>/storage/advanced-reports.
	StoragePath string
	// SigningSecret is REPORT_DOWNLOAD_SIGNING_SECRET, trimmed.
	SigningSecret string
	// PublicAPIBaseURL is PUBLIC_API_BASE_URL, with "/" trimmed from both
	// ends as Vapor did.
	PublicAPIBaseURL string
	// GotenbergBaseURL is GOTENBERG_BASE_URL (default http://gotenberg:3000).
	GotenbergBaseURL string
	// WorkerIntervalSeconds is ADVANCED_REPORT_WORKER_INTERVAL_SECONDS
	// (default 10, at least 2).
	WorkerIntervalSeconds int64
	// CleanupIntervalSeconds is ADVANCED_REPORT_CLEANUP_INTERVAL_SECONDS
	// (default 3600, at least 60).
	CleanupIntervalSeconds int64
}

func envInt64(name string, def int64) int64 {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// LoadSettings reads the reporting variables.
func LoadSettings() Settings {
	storage, ok := os.LookupEnv("ADVANCED_REPORT_STORAGE_PATH")
	if !ok {
		wd, _ := os.Getwd()
		storage = filepath.Join(wd, "storage", "advanced-reports")
	}
	gotenberg, ok := os.LookupEnv("GOTENBERG_BASE_URL")
	if !ok {
		gotenberg = "http://gotenberg:3000"
	}
	return Settings{
		StoragePath:            storage,
		SigningSecret:          strings.TrimSpace(os.Getenv("REPORT_DOWNLOAD_SIGNING_SECRET")),
		PublicAPIBaseURL:       strings.Trim(os.Getenv("PUBLIC_API_BASE_URL"), "/"),
		GotenbergBaseURL:       gotenberg,
		WorkerIntervalSeconds:  envInt64("ADVANCED_REPORT_WORKER_INTERVAL_SECONDS", 10),
		CleanupIntervalSeconds: envInt64("ADVANCED_REPORT_CLEANUP_INTERVAL_SECONDS", 3600),
	}
}

// signer validates the secret as configure.swift did: production refuses
// to boot with fewer than 32 characters, elsewhere an empty secret falls
// back to the development one.
func (s Settings) signer(production bool) (Signer, error) {
	if production && len([]rune(s.SigningSecret)) < 32 {
		return Signer{}, errors.New("REPORT_DOWNLOAD_SIGNING_SECRET must contain at least 32 characters in production")
	}
	if s.SigningSecret == "" {
		return Signer{Secret: devSigningSecret}, nil
	}
	return Signer{Secret: s.SigningSecret}, nil
}

// Feature mounts the reporting routes. Zero values read the environment;
// tests override them.
type Feature struct {
	Settings     *Settings
	Entitlements *entitlements.Config
	Now          func() time.Time
}

// Name implements server.Feature.
func (Feature) Name() string { return "reporting" }

type handlers struct {
	deps     *server.Deps
	q        *reportingdb.Queries
	ent      *entitlements.Service
	storage  Storage
	signer   Signer
	settings Settings
	now      func() time.Time
}

func (f Feature) settings() Settings {
	if f.Settings != nil {
		return *f.Settings
	}
	return LoadSettings()
}

func newEntitlements(d *server.Deps, cfg *entitlements.Config) *entitlements.Service {
	if cfg != nil {
		return entitlements.New(d.DB, *cfg)
	}
	return entitlements.NewFromEnv(d.DB)
}

// Register implements server.Feature, mirroring AdvancedReportingController.boot.
func (f Feature) Register(d *server.Deps, r *server.Routes) error {
	s := f.settings()
	signer, err := s.signer(d.Config.IsProduction())
	if err != nil {
		return err
	}
	now := f.Now
	if now == nil {
		now = time.Now
	}
	h := &handlers{
		deps:     d,
		q:        reportingdb.New(d.DB),
		ent:      newEntitlements(d, f.Entitlements),
		storage:  Storage{Root: s.StoragePath},
		signer:   signer,
		settings: s,
		now:      now,
	}

	// routes.grouped(ScopedBearerAuthenticator(), SessionToken.guardMiddleware())
	// then ScopeRequirementMiddleware(.exportRead / .exportWrite), per route
	// (a group's Use would register catch-all routes under /v1/reporting).
	read := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeExportRead)}
	write := []echo.MiddlewareFunc{d.Auth.Scoped(), auth.Guard(), auth.RequireScope(auth.ScopeExportWrite)}
	g := r.V1.Group("/reporting")

	g.GET("/templates", h.listTemplates, read...)
	g.GET("/templates/:templateId", h.getTemplate, read...)
	g.POST("/templates", h.createTemplate, write...)
	g.PUT("/templates/:templateId", h.updateTemplate, write...)
	g.DELETE("/templates/:templateId", h.archiveTemplate, write...)

	g.GET("/schedules", h.listSchedules, read...)
	g.GET("/schedules/:scheduleId", h.getSchedule, read...)
	g.POST("/schedules", h.createSchedule, write...)
	g.PUT("/schedules/:scheduleId", h.updateSchedule, write...)
	g.DELETE("/schedules/:scheduleId", h.deleteSchedule, write...)

	g.GET("/runs", h.listRuns, read...)
	g.GET("/runs/:runId", h.getRun, read...)
	g.POST("/runs", h.createRun, write...)

	g.GET("/artifacts/:artifactId/link", h.artifactLink, read...)

	// Unauthenticated by design: the download URL is itself a signed link.
	g.GET("/artifacts/:artifactId/download", h.downloadArtifact)
	return nil
}

// statusGone is Abort(.gone).
const statusGone = http.StatusGone
