package articles

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func (k *kit) upload(m member, data []byte) reply {
	k.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="c.png"`)
	hdr.Set("Content-Type", "image/png")
	part, _ := w.CreatePart(hdr)
	_, _ = part.Write(data)
	_ = w.Close()
	return k.raw("POST", "/v1/articles/images", m.token, w.FormDataContentType(), buf.Bytes())
}

func (k *kit) raw(method, path, token, contentType string, body []byte) reply {
	k.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, k.app.Server.URL+path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := k.app.Server.Client().Do(req)
	if err != nil {
		k.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, header: resp.Header, body: data}
}

func (k *kit) uploadID(m member, data []byte) uuid.UUID {
	k.t.Helper()
	r := k.upload(m, data)
	expectStatus(k.t, r, 200)
	var res struct{ ID string }
	k.json(r, &res)
	return uuid.MustParse(res.ID)
}

func (k *kit) image(id uuid.UUID, token string) reply {
	return k.send("GET", "/v1/articles/images/"+strings.ToUpper(id.String()), token, nil, nil)
}

func TestCoverRoundTrip(t *testing.T) {
	k := newKit(t, true)
	ana := k.member("img_ana")
	png := pngBytes(1200, 630)
	r := k.upload(ana, png)
	expectStatus(t, r, 200)
	if !bytes.HasPrefix(r.body, []byte(`{"id":"`)) {
		t.Fatalf("%s", r.body)
	}
	var res struct{ ID string }
	k.json(r, &res)
	d := k.publish(ana, articleInput().with("coverImageId", res.ID))
	if d.Article.CoverImageID == nil || d.Article.CoverImageID.String() != res.ID {
		t.Fatalf("%+v", d.Article)
	}
	got := k.image(uuid.MustParse(res.ID), ana.token)
	expectStatus(t, got, 200)
	if got.header.Get("Content-Type") != "image/png" || got.header.Get("Cache-Control") != "public, max-age=86400" ||
		got.header.Get("X-Content-Type-Options") != "nosniff" || !bytes.Equal(got.body, png) {
		t.Fatalf("%v", got.header)
	}
	var w, h int
	_ = k.app.Deps.DB.QueryRow(context.Background(), `SELECT width, height FROM article_images`).Scan(&w, &h)
	if w != 1200 || h != 630 {
		t.Fatalf("%dx%d", w, h)
	}
}

func TestUploadRejects(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("rej_ana"), k.member("rej_bo")
	r := k.upload(ana, []byte("<svg onload=alert(1)>"))
	if r.status != 415 || r.reason() != "Use a JPEG, PNG or WebP image." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	id := k.uploadID(ana, pngBytes(10, 10))
	r = k.send("POST", "/v1/articles", bo.token, articleInput().with("coverImageId", id.String()), nil)
	if r.status != 400 || r.reason() != "Upload the cover image again." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	r = k.send("POST", "/v1/articles", bo.token, articleInput().with("coverImageId", uuid.NewString()), nil)
	expectStatus(t, r, 400)
	// Over 2 MB of image, under the 3mb body cap.
	r = k.upload(ana, append(pngBytes(10, 10), make([]byte, 2_000_001)...))
	if r.status != 413 || r.reason() != "Images must be 2 MB or smaller." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// A body reaching 3mb is refused before the handler.
	r = k.upload(ana, make([]byte, 3<<20))
	if r.status != 413 || r.reason() != "Payload Too Large" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	// The JSON shape of Vapor's File works too.
	body := `{"file":{"data":"` + base64.StdEncoding.EncodeToString(pngBytes(20, 20)) + `","filename":"x.png"}}`
	expectStatus(t, k.raw("POST", "/v1/articles/images", ana.token, "application/json", []byte(body)), 200)
	// Decode failures.
	for _, c := range []struct {
		ct, body string
		status   int
		reason   string
	}{
		{"", "x", 415, "Can't decode data without a content type"},
		{"multipart/form-data", "x", 415, "Unsupported Media Type"},
		{"multipart/form-data; boundary=b", "", 422, "Unprocessable Entity"},
		{"multipart/form-data; boundary=b", "--b\r\nContent-Disposition: form-data; name=\"other\"\r\n\r\nx\r\n--b--\r\n", 400, `No such key 'file' at path ''. No value associated with key "file".`},
		{"multipart/form-data; boundary=b", "--b\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nx\r\n--b--\r\n", 400, `Data corrupted at path 'file'. Could not convert value at "file" to type File from multipart part.`},
		{"application/json", `{}`, 400, `No such key 'file' at path ''. No value associated with key CodingKeys(stringValue: "file", intValue: nil) ("file").`},
	} {
		r := k.raw("POST", "/v1/articles/images", ana.token, c.ct, []byte(c.body))
		if r.status != c.status || r.reason() != c.reason {
			t.Fatalf("%q: %d %s", c.ct, r.status, r.body)
		}
	}
}

// backdate moves an owner's uploads 25 hours into the past.
func (k *kit) backdate(owner member, except *uuid.UUID) {
	k.exec(`UPDATE article_images SET created_at = created_at - interval '25 hours'
		WHERE owner_id = $1 AND id IS DISTINCT FROM $2`, owner.id, except)
}

func TestDailyUploadCap(t *testing.T) {
	k := newKit(t, true)
	admin, ana := k.admin("cap_admin"), k.member("cap_ana")
	png := pngBytes(10, 10)
	for range 10 {
		expectStatus(t, k.upload(ana, png), 200)
		expectStatus(t, k.upload(admin, png), 200)
	}
	r := k.upload(ana, png)
	if r.status != 429 || r.code() != "article_image_daily_limit" || r.reason() != "You can upload 10 images a day." {
		t.Fatalf("%d %s", r.status, r.body)
	}
	expectStatus(t, k.upload(admin, png), 200)
	// The window is rolling: uploads older than a day stop counting.
	k.backdate(ana, nil)
	expectStatus(t, k.upload(ana, png), 200)
}

func (k *kit) imageExists(id uuid.UUID) bool {
	var n int
	_ = k.app.Deps.DB.QueryRow(context.Background(), `SELECT count(*) FROM article_images WHERE id = $1`, id).Scan(&n)
	return n == 1
}

func TestUploadCleansUpOrphans(t *testing.T) {
	k := newKit(t, true)
	ana, bo := k.member("orph_ana"), k.member("orph_bo")
	png := pngBytes(10, 10)
	orphan := k.uploadID(ana, png)
	cover := k.uploadID(ana, png)
	deletedCover := k.uploadID(ana, png)
	fresh := k.uploadID(ana, png)
	bosOrphan := k.uploadID(bo, png)
	k.publish(ana, articleInput().with("coverImageId", cover.String()))
	deleted := k.publish(ana, articleInput().with("title", "Second article about NEXT").with("coverImageId", deletedCover.String()))
	expectStatus(t, k.send("DELETE", "/v1/articles/"+deleted.Article.Code, ana.token, nil, nil), 204)
	k.backdate(ana, &fresh)
	k.backdate(bo, nil)
	expectStatus(t, k.upload(ana, png), 200)
	if k.imageExists(orphan) || !k.imageExists(cover) || !k.imageExists(deletedCover) || !k.imageExists(fresh) || !k.imageExists(bosOrphan) {
		t.Fatal("sweep removed the wrong images")
	}
}

func TestCoverServing(t *testing.T) {
	k := newKit(t, true)
	admin, ana, bo := k.admin("srv_admin"), k.member("srv_ana"), k.member("srv_bo")
	_, web := k.credential(admin)
	png := pngBytes(10, 10)
	used := k.uploadID(ana, png)
	loose := k.uploadID(ana, png)
	code := k.publish(ana, articleInput().with("coverImageId", used.String())).Article.Code

	pub := k.image(used, web)
	expectStatus(t, pub, 200)
	if pub.header.Get("Cache-Control") != "public, max-age=86400" {
		t.Fatal(pub.header)
	}
	expectStatus(t, k.image(used, bo.token), 200)
	expectStatus(t, k.image(loose, bo.token), 404)
	expectStatus(t, k.image(loose, web), 404)
	own := k.image(loose, ana.token)
	expectStatus(t, own, 200)
	// Never into a shared cache: it would outlive the owner-only check.
	if own.header.Get("Cache-Control") != "private, no-store" {
		t.Fatal(own.header)
	}
	expectStatus(t, k.image(loose, admin.token), 200)

	k.exec(`UPDATE articles SET status = 'hidden' WHERE code = $1`, code)
	expectStatus(t, k.image(used, web), 404)
	expectStatus(t, k.image(used, bo.token), 404)
	expectStatus(t, k.image(used, ana.token), 200)
	expectStatus(t, k.image(uuid.New(), ana.token), 404)
	r := k.send("GET", "/v1/articles/images/not-a-uuid", ana.token, nil, nil)
	if r.status != 404 || r.reason() != "Not Found" {
		t.Fatalf("%d %s", r.status, r.body)
	}
}

func TestHiddenIsAuthorAndAdminOnly(t *testing.T) {
	k := newKit(t, true)
	admin, ana, bo := k.admin("mod_admin"), k.member("mod_ana"), k.member("mod_bo")
	code := k.publish(ana, articleInput()).Article.Code
	k.exec(`UPDATE articles SET status = 'hidden' WHERE code = $1`, code)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, bo.token, nil, nil), 404)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, ana.token, nil, nil), 200)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, admin.token, nil, nil), 200)
	// Owner decision: a hidden article is locked for its author (Vapor let
	// them edit it; see TestEditHiddenOrDeletedArticle). Votes and views
	// need it published.
	expectStatus(t, k.send("PATCH", "/v1/articles/"+code, ana.token, articleInput(), nil), 403)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/vote", ana.token, nil, nil), 404)
	expectStatus(t, k.send("POST", "/v1/articles/"+code+"/view", ana.token, nil, nil), 404)
	var l listResponse
	k.json(k.send("GET", "/v1/articles", bo.token, nil, nil), &l)
	if len(l.Items) != 0 {
		t.Fatal("hidden in feed")
	}
	k.exec(`UPDATE articles SET status = 'published' WHERE code = $1`, code)
	expectStatus(t, k.send("GET", "/v1/articles/"+code, bo.token, nil, nil), 200)
}

// --- ArticleImageSnifferTests ------------------------------------------------

func jpegBytes(w, h uint16) []byte {
	b := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x04, 0x00, 0x00, 0xFF, 0xC0, 0x00, 0x11, 0x08}
	b = binary.BigEndian.AppendUint16(b, h)
	b = binary.BigEndian.AppendUint16(b, w)
	return append(b, 0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01)
}

func webpBytes(w, h uint16) []byte {
	b := append([]byte("RIFF"), 0x24, 0, 0, 0)
	b = append(b, []byte("WEBPVP8 ")...)
	b = append(b, 0x18, 0, 0, 0, 0x30, 0x01, 0x00, 0x9D, 0x01, 0x2A)
	b = binary.LittleEndian.AppendUint16(b, w)
	b = binary.LittleEndian.AppendUint16(b, h)
	return append(b, make([]byte, 8)...)
}

func TestSnifferKnownTypes(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		want sniffedImage
	}{
		{pngBytes(1200, 630), sniffedImage{"image/png", 1200, 630}},
		{jpegBytes(800, 600), sniffedImage{"image/jpeg", 800, 600}},
		{webpBytes(640, 480), sniffedImage{"image/webp", 640, 480}},
	} {
		got, err := sniffImage(c.b)
		if err != nil || got != c.want {
			t.Fatalf("%+v %v, want %+v", got, err, c.want)
		}
	}
	// JPEG fill bytes (repeated 0xFF) before a marker are skipped.
	b := []byte{0xFF, 0xD8, 0xFF, 0xFF, 0xFF, 0xC0, 0x00, 0x11, 0x08}
	b = binary.BigEndian.AppendUint16(b, 480)
	b = binary.BigEndian.AppendUint16(b, 640)
	b = append(b, 0x03, 0x01, 0x22, 0x00, 0x02, 0x11, 0x01, 0x03, 0x11, 0x01)
	if got, err := sniffImage(b); err != nil || got != (sniffedImage{"image/jpeg", 640, 480}) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestSnifferLimits(t *testing.T) {
	status := func(b []byte) int {
		_, err := sniffImage(b)
		if err == nil {
			return 0
		}
		return err.(interface{ StatusCode() int }).StatusCode()
	}
	if s := status([]byte("<svg onload=alert(1)>")); s != 415 {
		t.Fatal(s)
	}
	if s := status(append(pngBytes(10, 10), make([]byte, 2_000_001)...)); s != 413 {
		t.Fatal(s)
	}
	if s := status(pngBytes(5000, 10)); s != 400 {
		t.Fatal(s)
	}
	if s := status(pngBytes(0, 10)); s != 400 {
		t.Fatal(s)
	}
	if s := status([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}); s != 400 {
		t.Fatal(s)
	}
}
