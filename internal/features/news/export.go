package news

import (
	"sync"

	"norviq-api/internal/server"
)

// ScopeForYou is ThesisWatchScope.forYou.
const ScopeForYou = scopeForYou

var shared sync.Map // *server.Deps -> *Service

// SharedService is app.thesisWatchService for other ports (the Telegram
// /news command): the service the controller builds, once per Deps.
func SharedService(d *server.Deps) (*Service, error) {
	if v, ok := shared.Load(d); ok {
		return v.(*Service), nil
	}
	s, err := Feature{}.service(d)
	if err != nil {
		return nil, err
	}
	v, _ := shared.LoadOrStore(d, s)
	return v.(*Service), nil
}
