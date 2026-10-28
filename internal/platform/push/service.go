package push

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"norviq-api/internal/server"
	"norviq-api/internal/store/pushdb"

	"github.com/google/uuid"
)

// Service is the push stack one process shares: the sender and the device
// registry. Every domain that pushes gets it with From(d).
type Service struct {
	Sender  Sender
	Devices *Devices
	// Config is nil when APNS is unconfigured.
	Config *Config
}

// New builds the service the way configureAPNS did: Noop when the APNS_*
// variables are incomplete; with a key that does not parse, a boot error in
// production and Noop (with a warning) anywhere else.
func New(cfg *Config, q pushdb.Querier, logger *slog.Logger, production bool) (*Service, error) {
	devices := &Devices{Q: q}
	if cfg == nil {
		logger.Warn("APNS is disabled. Configure APNS_TEAM_ID, APNS_KEY_ID, APNS_PRIVATE_KEY_P8 and APNS_TOPIC to enable push notifications.")
		return &Service{Sender: Noop{Logger: logger}, Devices: devices}, nil
	}
	key, err := cfg.PrivateKey()
	if err != nil {
		if production {
			return nil, fmt.Errorf("APNS_PRIVATE_KEY_P8 could not be parsed: %w", err)
		}
		logger.Warn("APNS is disabled because APNS_PRIVATE_KEY_P8 could not be parsed", "error", err)
		return &Service{Sender: Noop{Logger: logger}, Devices: devices, Config: cfg}, nil
	}
	return &Service{Sender: NewAPNS(*cfg, key, devices, logger), Devices: devices, Config: cfg}, nil
}

var services sync.Map // *server.Deps -> *Service

// From returns the process's push service, building it from the environment
// on first use. Features call it from Register, so a production boot with a
// bad key fails there, as Vapor's configure did.
func From(d *server.Deps) (*Service, error) {
	if v, ok := services.Load(d); ok {
		return v.(*Service), nil
	}
	s, err := New(ConfigFromEnv(), pushdb.New(d.DB), d.Logger, d.Config.IsProduction())
	if err != nil {
		return nil, err
	}
	v, _ := services.LoadOrStore(d, s)
	return v.(*Service), nil
}

// Install sets the service for d; tests use it to inject a fake sender.
func Install(d *server.Deps, s *Service) { services.Store(d, s) }

// SendToUser sends n to every active device of userID. No devices is not an
// error: the summary is zero.
func (s *Service) SendToUser(ctx context.Context, userID uuid.UUID, n Notification) (Summary, error) {
	devices, err := s.Devices.Active(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	if len(devices) == 0 {
		return Summary{}, nil
	}
	return s.Sender.Send(ctx, n, devices), nil
}

func ptr[T any](v T) *T { return &v }

// HealthCheck is routes.swift's apnsHealthCheck.
func (s *Service) HealthCheck(context.Context) server.HealthCheck {
	if !s.Sender.Enabled() {
		return server.HealthCheck{Status: "skipped", Message: ptr("APNS is disabled; push delivery is unavailable.")}
	}
	if s.Config == nil {
		return server.HealthCheck{Status: "skipped", Message: ptr("APNS is not configured; push delivery is disabled.")}
	}
	if _, err := s.Config.PrivateKey(); err != nil {
		return server.HealthCheck{Status: "unhealthy", Message: ptr("APNS private key could not be parsed.")}
	}
	return server.HealthCheck{Status: "healthy"}
}
