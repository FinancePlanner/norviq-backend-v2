package social

import (
	"slices"
	"testing"
)

// Owner decision (docs/vapor-bugs/social.md): the web app id goes first in
// FACEBOOK_APP_ID, with FACEBOOK_APP_SECRET as that app's secret. Pin the
// rule the deploy docs rely on: the first listed id runs the web flow, and
// every id stays a Limited Login audience.
func TestFacebookWebAppIDIsTheFirstListed(t *testing.T) {
	t.Setenv("FACEBOOK_APP_ID", " web-app , ios-app,,")
	t.Setenv("FACEBOOK_APP_SECRET", "web-secret")
	cfg := facebookConfigFromEnv()
	if got := cfg.webAppID(); got != "web-app" {
		t.Fatalf("webAppID = %q, want the first id", got)
	}
	if !slices.Equal(cfg.AppIDs, []string{"web-app", "ios-app"}) {
		t.Fatalf("AppIDs = %v", cfg.AppIDs)
	}
	if cfg.AppSecret == nil || *cfg.AppSecret != "web-secret" {
		t.Fatalf("AppSecret = %v", cfg.AppSecret)
	}
	t.Setenv("FACEBOOK_APP_ID", "")
	if got := facebookConfigFromEnv().webAppID(); got != "" {
		t.Fatalf("no ids: webAppID = %q", got)
	}
}
