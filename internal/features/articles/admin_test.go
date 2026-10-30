package articles

import "testing"

// Ported from ArticleImagesAndModerationTests.hiddenIsAuthorAndAdminOnly
// and ArticlesRouteTests (flag off).
func TestAdminVisibility(t *testing.T) {
	k := newKit(t, true)
	admin := k.admin("mod_admin")
	ana, bo := k.member("mod_ana"), k.member("mod_bo")
	code := k.publish(ana, articleInput()).Article.Code
	path := "/v1/admin/articles/" + code + "/visibility"

	r := k.send("PUT", path, bo.token, map[string]any{"hidden": true}, nil)
	if r.status != 403 || r.reason() != "Admin access required." {
		t.Fatalf("member: %d %s", r.status, r.body)
	}
	expectStatus(t, k.send("PUT", path, admin.token, map[string]any{"hidden": true}, nil), 204)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, bo.token, nil, nil), 404)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, ana.token, nil, nil), 200)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, admin.token, nil, nil), 200)
	var l listResponse
	k.json(k.send("GET", "/v1/articles", bo.token, nil, nil), &l)
	if len(l.Items) != 0 {
		t.Fatalf("hidden in feed: %v", codes(l.Items))
	}
	expectStatus(t, k.send("PUT", path, admin.token, map[string]any{"hidden": false}, nil), 204)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, bo.token, nil, nil), 200)

	// Unknown and deleted articles are 404; a missing body is a decode error.
	expectStatus(t, k.send("PUT", "/v1/admin/articles/nope/visibility", admin.token, map[string]any{"hidden": true}, nil), 404)
	expectStatus(t, k.send("PUT", path, admin.token, map[string]any{}, nil), 400)
	expectStatus(t, k.send("DELETE", "/v1/articles/"+code, ana.token, nil, nil), 204)
	expectStatus(t, k.send("PUT", path, admin.token, map[string]any{"hidden": false}, nil), 404)
}

func TestAdminVisibilityFlagOff(t *testing.T) {
	k := newKit(t, false)
	admin := k.admin("off_admin")
	expectStatus(t, k.send("PUT", "/v1/admin/articles/anything/visibility", admin.token, map[string]any{"hidden": true}, nil), 404)
}
