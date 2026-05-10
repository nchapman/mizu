package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nchapman/mizu/internal/auth"
	"github.com/nchapman/mizu/internal/config"
	mizudb "github.com/nchapman/mizu/internal/db"
	"github.com/nchapman/mizu/internal/feeds"
	"github.com/nchapman/mizu/internal/media"
	"github.com/nchapman/mizu/internal/post"
	"github.com/nchapman/mizu/internal/webmention"
)

type harness struct {
	srv    *Server
	router http.Handler
	auth   *auth.Service
	posts  *post.Store
	feeds  *feeds.Service
	media  *media.Store
	wm     *webmention.Service
	wmStr  *webmention.Store
	cfg    *config.Config
}

const fixtureIndexHTML = `<!doctype html><html><body data-app="mizu"></body></html>`

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	contentDir := filepath.Join(dir, "content")
	if err := os.MkdirAll(filepath.Join(contentDir, "posts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(contentDir, "drafts"), 0o755); err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(dir, "media")
	stateDir := filepath.Join(dir, "state")
	for _, d := range []string{mediaDir, stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	posts, err := post.NewStore(contentDir)
	if err != nil {
		t.Fatal(err)
	}
	mediaStore, err := media.NewStore(mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := mizudb.Open(filepath.Join(stateDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	a, err := auth.New(conn)
	if err != nil {
		t.Fatal(err)
	}
	feedStore := feeds.NewStore(conn)
	feedSvc := feeds.NewService(feedStore, filepath.Join(stateDir, "subs.opml"), "Test")
	// Bypass safehttp for httptest URLs.
	feedSvc.SetValidateForTest(func(_ context.Context, raw string) (string, error) {
		if strings.TrimSpace(raw) == "" {
			return "", feeds.ErrInvalidURL
		}
		return raw, nil
	})
	poller := feeds.NewPoller(feedStore, 0, "test/1.0")
	feeds.SetPollerHTTPForTest(poller, http.DefaultClient)

	wmStore := webmention.NewStore(conn)
	wm := webmention.New(wmStore, "https://example.test")

	cfg := &config.Config{
		Site:  config.Site{Title: "Test", BaseURL: "https://example.test"},
		Paths: config.Paths{},
	}
	cfg.ApplyDefaults()
	dist := fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte(fixtureIndexHTML)},
		"assets/app.js": &fstest.MapFile{Data: []byte("console.log('hi')")},
	}
	srv := New(context.Background(), cfg, posts, feedSvc, poller, a, mediaStore, wm, dist)
	r := chi.NewRouter()
	r.Route("/admin", func(r chi.Router) { srv.Routes(r) })

	return &harness{
		srv: srv, router: r, auth: a, posts: posts, feeds: feedSvc,
		media: mediaStore, wm: wm, wmStr: wmStore, cfg: cfg,
	}
}

// login runs first-run setup with a fixed email/password and returns
// a session cookie ready to attach to subsequent requests.
func (h *harness) login(t *testing.T) *http.Cookie {
	t.Helper()
	tok := h.auth.SetupToken()
	u, err := h.auth.Setup(context.Background(), tok, "alice@example.com", "hunter22pw", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := h.auth.CreateSession(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.CookieName, Value: sess}
}

func (h *harness) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		switch v := body.(type) {
		case string:
			rdr = strings.NewReader(v)
		case []byte:
			rdr = bytes.NewReader(v)
		default:
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			rdr = bytes.NewReader(b)
		}
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		if _, ok := body.(string); !ok {
			if _, ok := body.([]byte); !ok {
				req.Header.Set("Content-Type", "application/json")
			}
		}
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	return w
}

// --- public endpoints ---

func TestMe_FreshIsUnconfigured(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "GET", "/admin/api/me", nil, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	var got map[string]any
	json.NewDecoder(w.Body).Decode(&got)
	if got["configured"] != false || got["authenticated"] != false {
		t.Errorf("got %+v, want both false", got)
	}
	if got["site_title"] != "Test" {
		t.Errorf("site_title=%v, want Test", got["site_title"])
	}
}

func TestSetup_HappyPath(t *testing.T) {
	h := newHarness(t)
	tok := h.auth.SetupToken()
	w := h.do(t, "POST", "/admin/api/setup",
		map[string]string{
			"email":        "alice@example.com",
			"password":     "hunter22pw",
			"display_name": "Alice",
			"token":        tok,
		}, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("setup did not set session cookie")
	}
	// /me with the cookie should now show authenticated + user.
	req := httptest.NewRequest("GET", "/admin/api/me", nil)
	req.AddCookie(cookies[0])
	w2 := httptest.NewRecorder()
	h.router.ServeHTTP(w2, req)
	var got map[string]any
	json.NewDecoder(w2.Body).Decode(&got)
	if got["configured"] != true || got["authenticated"] != true {
		t.Errorf("after setup: got %+v", got)
	}
	user, ok := got["user"].(map[string]any)
	if !ok {
		t.Fatalf("/me did not return user: %+v", got)
	}
	if user["email"] != "alice@example.com" || user["display_name"] != "Alice" {
		t.Errorf("user payload = %+v", user)
	}
}

func TestSetup_RejectsBadToken(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "POST", "/admin/api/setup",
		map[string]string{"email": "a@b.com", "password": "hunter22pw", "token": "wrong"}, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("code=%d, want 403", w.Code)
	}
}

func TestSetup_RejectsShortPassword(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "POST", "/admin/api/setup",
		map[string]string{"email": "a@b.com", "password": "x", "token": h.auth.SetupToken()}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d, want 400", w.Code)
	}
}

func TestSetup_RejectsInvalidEmail(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "POST", "/admin/api/setup",
		map[string]string{"email": "not an email", "password": "hunter22pw", "token": h.auth.SetupToken()}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d, want 400", w.Code)
	}
}

func TestLogin_BeforeSetupConflicts(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "POST", "/admin/api/login", map[string]string{"email": "a@b.com", "password": "x"}, nil)
	if w.Code != http.StatusConflict {
		t.Errorf("code=%d, want 409", w.Code)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	w := h.do(t, "POST", "/admin/api/login",
		map[string]string{"email": "alice@example.com", "password": "wrong-password"}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", w.Code)
	}
}

func TestLogin_UnknownEmailReturnsSame401(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	w := h.do(t, "POST", "/admin/api/login",
		map[string]string{"email": "ghost@example.com", "password": "anything22"}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401 (must not 404 or 409, which would leak existence)", w.Code)
	}
}

func TestLogin_HappyPath(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	w := h.do(t, "POST", "/admin/api/login",
		map[string]string{"email": "alice@example.com", "password": "hunter22pw"}, nil)
	if w.Code != http.StatusNoContent {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) == 0 {
		t.Error("login did not set cookie")
	}
}

func TestLogin_OversizeBodyRejected(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	pad := strings.Repeat("a", int(h.cfg.Limits.Body.Login)+1)
	big := `{"email":"alice@example.com","password":"` + pad + `"}`
	w := h.do(t, "POST", "/admin/api/login", big, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code=%d, want 413", w.Code)
	}
}

func TestLogin_RateLimit(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	limit := h.cfg.Limits.Rate.Login.Requests
	for i := 0; i < limit; i++ {
		w := h.do(t, "POST", "/admin/api/login",
			map[string]string{"email": "alice@example.com", "password": "hunter22pw"}, nil)
		if w.Code == http.StatusTooManyRequests {
			t.Fatalf("hit 429 too early at request %d", i)
		}
	}
	w := h.do(t, "POST", "/admin/api/login",
		map[string]string{"email": "alice@example.com", "password": "hunter22pw"}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("code=%d, want 429 after %d requests", w.Code, limit)
	}
}

func TestLogin_LockoutAfterFailures(t *testing.T) {
	h := newHarness(t)
	h.login(t)
	const max = 5
	for i := 0; i < max; i++ {
		h.do(t, "POST", "/admin/api/login",
			map[string]string{"email": "alice@example.com", "password": "wrong"}, nil)
	}
	w := h.do(t, "POST", "/admin/api/login",
		map[string]string{"email": "alice@example.com", "password": "hunter22pw"}, nil)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("code=%d, want 429 (locked)", w.Code)
	}
	if w.Result().Header.Get("Retry-After") == "" {
		t.Error("missing Retry-After header on lockout")
	}
}

func TestUsers_CRUDAndDeleteSelfCascades(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	// Add a second user.
	w := h.do(t, "POST", "/admin/api/users",
		map[string]string{"email": "bob@example.com", "password": "hunter22pw", "display_name": "Bob"}, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", w.Code, w.Body.String())
	}
	var bob userDTO
	json.NewDecoder(w.Body).Decode(&bob)
	if bob.Email != "bob@example.com" {
		t.Errorf("bob.Email=%q", bob.Email)
	}

	// Duplicate email → 409.
	w = h.do(t, "POST", "/admin/api/users",
		map[string]string{"email": "BOB@example.com", "password": "another22"}, c)
	if w.Code != http.StatusConflict {
		t.Errorf("duplicate code=%d, want 409", w.Code)
	}

	// List shows two users.
	w = h.do(t, "GET", "/admin/api/users", nil, c)
	var list []userDTO
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 2 {
		t.Errorf("list len=%d", len(list))
	}

	// Delete Bob.
	w = h.do(t, "DELETE", "/admin/api/users/"+strconv.FormatInt(bob.ID, 10), nil, c)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete code=%d body=%s", w.Code, w.Body.String())
	}
	// (The "can't delete the last user" rule is enforced at the service
	// layer and covered in internal/auth tests. The handler-level
	// self-delete guard short-circuits before reaching it, so we can't
	// exercise that branch from here.)
}

func TestUsers_RejectsSelfDelete(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "GET", "/admin/api/users", nil, c)
	var list []userDTO
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 {
		t.Fatalf("expected 1 user after login, got %d", len(list))
	}
	w = h.do(t, "DELETE", "/admin/api/users/"+strconv.FormatInt(list[0].ID, 10), nil, c)
	if w.Code != http.StatusForbidden {
		t.Errorf("self-delete code=%d, want 403", w.Code)
	}
}

func TestChangeOwnPassword(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/me/password",
		map[string]string{"old_password": "wrong", "new_password": "newpassword22"}, c)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong old code=%d, want 401", w.Code)
	}
	w = h.do(t, "POST", "/admin/api/me/password",
		map[string]string{"old_password": "hunter22pw", "new_password": "newpassword22"}, c)
	if w.Code != http.StatusNoContent {
		t.Errorf("happy path code=%d body=%s", w.Code, w.Body.String())
	}
	// Old password should no longer work.
	w = h.do(t, "POST", "/admin/api/login",
		map[string]string{"email": "alice@example.com", "password": "hunter22pw"}, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("old password still works: code=%d", w.Code)
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/logout", nil, c)
	if w.Code != http.StatusNoContent {
		t.Fatalf("code=%d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].MaxAge >= 0 {
		t.Errorf("logout cookie not cleared: %+v", cookies)
	}
}

// --- auth gate ---

func TestAuthGate_BlocksAllProtectedRoutes(t *testing.T) {
	h := newHarness(t)
	for _, route := range []struct{ method, path string }{
		{"GET", "/admin/api/posts"},
		{"POST", "/admin/api/posts"},
		{"GET", "/admin/api/drafts"},
		{"GET", "/admin/api/subscriptions"},
		{"GET", "/admin/api/stream"},
		{"POST", "/admin/api/media"},
		{"GET", "/admin/api/mentions"},
	} {
		w := h.do(t, route.method, route.path, nil, nil)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: code=%d, want 401", route.method, route.path, w.Code)
		}
	}
}

// --- posts ---

func TestPosts_CRUD(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	// Create.
	w := h.do(t, "POST", "/admin/api/posts",
		map[string]any{"title": "First Post", "body": "hello"}, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create code=%d body=%s", w.Code, w.Body.String())
	}
	var created postDTO
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || !strings.Contains(created.HTML, "<p>hello</p>") {
		t.Errorf("created=%+v", created)
	}
	if !strings.Contains(created.Path, "/first-post") {
		t.Errorf("path=%q does not contain frozen slug", created.Path)
	}

	// List.
	w = h.do(t, "GET", "/admin/api/posts", nil, c)
	var list []postDTO
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Errorf("list = %+v", list)
	}

	// Update — slug must stay frozen even when the title changes.
	w = h.do(t, "PATCH", "/admin/api/posts/"+created.ID,
		map[string]any{"title": "Renamed Title", "body": "updated"}, c)
	if w.Code != 200 {
		t.Fatalf("update code=%d body=%s", w.Code, w.Body.String())
	}
	var updated postDTO
	json.NewDecoder(w.Body).Decode(&updated)
	if updated.Path != created.Path {
		t.Errorf("path drifted: %q vs %q", updated.Path, created.Path)
	}
	if updated.Title != "Renamed Title" {
		t.Errorf("title=%q", updated.Title)
	}

	// Update with empty body must 400.
	w = h.do(t, "PATCH", "/admin/api/posts/"+created.ID,
		map[string]any{"title": "X", "body": "   "}, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty body update code=%d, want 400", w.Code)
	}

	// Delete.
	w = h.do(t, "DELETE", "/admin/api/posts/"+created.ID, nil, c)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete code=%d", w.Code)
	}
	w = h.do(t, "DELETE", "/admin/api/posts/"+created.ID, nil, c)
	if w.Code != http.StatusNotFound {
		t.Errorf("second delete code=%d, want 404", w.Code)
	}
}

func TestPosts_CreateRejectsEmptyBody(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/posts", map[string]any{"title": "x", "body": "  "}, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d", w.Code)
	}
}

func TestPosts_CreateBadJSON(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/posts", "not json", c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d", w.Code)
	}
}

// --- drafts ---

func TestDrafts_CreatePublishDeletes(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)

	// Create draft.
	w := h.do(t, "POST", "/admin/api/drafts",
		map[string]any{"title": "Maybe", "body": "still cooking"}, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create code=%d", w.Code)
	}
	var d draftDTO
	json.NewDecoder(w.Body).Decode(&d)

	// List.
	w = h.do(t, "GET", "/admin/api/drafts", nil, c)
	var list []draftDTO
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 {
		t.Fatalf("len=%d", len(list))
	}

	// Update.
	w = h.do(t, "PATCH", "/admin/api/drafts/"+d.ID,
		map[string]any{"title": "Better", "body": "now ready"}, c)
	if w.Code != 200 {
		t.Fatalf("update code=%d", w.Code)
	}

	// Publish — promotes to a real post.
	w = h.do(t, "POST", "/admin/api/drafts/"+d.ID+"/publish", nil, c)
	if w.Code != 200 {
		t.Fatalf("publish code=%d body=%s", w.Code, w.Body.String())
	}
	var p postDTO
	json.NewDecoder(w.Body).Decode(&p)
	if p.Title != "Better" {
		t.Errorf("published title=%q", p.Title)
	}

	// Draft list now empty.
	w = h.do(t, "GET", "/admin/api/drafts", nil, c)
	var list2 []draftDTO
	json.NewDecoder(w.Body).Decode(&list2)
	if len(list2) != 0 {
		t.Errorf("drafts after publish = %+v", list2)
	}
}

func TestDrafts_DeleteAndPublishNotFound(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "DELETE", "/admin/api/drafts/nope", nil, c)
	if w.Code != http.StatusNotFound {
		t.Errorf("delete missing code=%d", w.Code)
	}
	w = h.do(t, "POST", "/admin/api/drafts/nope/publish", nil, c)
	if w.Code != http.StatusNotFound {
		t.Errorf("publish missing code=%d", w.Code)
	}
}

// --- subscriptions ---

func TestSubscriptions_CRUD(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)

	// Empty list.
	w := h.do(t, "GET", "/admin/api/subscriptions", nil, c)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	var list []subscriptionDTO
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 0 {
		t.Errorf("initial list = %+v", list)
	}

	// Add.
	w = h.do(t, "POST", "/admin/api/subscriptions",
		map[string]any{"url": "https://a/feed", "title": "A"}, c)
	if w.Code != http.StatusCreated {
		t.Fatalf("add code=%d body=%s", w.Code, w.Body.String())
	}

	// List shows new feed.
	w = h.do(t, "GET", "/admin/api/subscriptions", nil, c)
	json.NewDecoder(w.Body).Decode(&list)
	if len(list) != 1 || list[0].URL != "https://a/feed" {
		t.Errorf("after add list = %+v", list)
	}

	// Remove (URL goes in query string).
	q := url.Values{"url": {"https://a/feed"}}.Encode()
	w = h.do(t, "DELETE", "/admin/api/subscriptions?"+q, nil, c)
	if w.Code != http.StatusNoContent {
		t.Fatalf("remove code=%d", w.Code)
	}
}

func TestSubscriptions_AddInvalidURL(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/subscriptions",
		map[string]any{"url": ""}, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d", w.Code)
	}
}

func TestSubscriptions_RemoveMissingURLParam(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "DELETE", "/admin/api/subscriptions", nil, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d", w.Code)
	}
}

// --- feed-item read state ---

func TestItemRead_MarkAndUnmark(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	ctx := context.Background()

	feedID, err := h.feeds.Store.UpsertFeed(ctx, &feeds.Feed{URL: "https://a/", Title: "A"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.feeds.Store.InsertItem(ctx, &feeds.Item{
		FeedID: feedID, GUID: "g1", Title: "T1", FetchedAt: nowUTC(),
	}); err != nil {
		t.Fatal(err)
	}
	items, err := h.feeds.Store.Timeline(ctx, feeds.TimelineCursor{}, 10, false)
	if err != nil || len(items) != 1 {
		t.Fatalf("seed timeline: err=%v len=%d", err, len(items))
	}
	idStr := jsonNum(items[0].ID)

	w := h.do(t, "POST", "/admin/api/items/"+idStr+"/read", nil, c)
	if w.Code != http.StatusNoContent {
		t.Fatalf("mark read code=%d body=%s", w.Code, w.Body.String())
	}
	w = h.do(t, "DELETE", "/admin/api/items/"+idStr+"/read", nil, c)
	if w.Code != http.StatusNoContent {
		t.Errorf("mark unread code=%d", w.Code)
	}
}

func TestItemRead_BadID(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/items/notanumber/read", nil, c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d", w.Code)
	}
}

func TestItemRead_MissingItem(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	w := h.do(t, "POST", "/admin/api/items/9999/read", nil, c)
	if w.Code != http.StatusNotFound {
		t.Errorf("code=%d", w.Code)
	}
}

// --- media ---

func TestUploadMedia_HappyPath(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	body, contentType := buildMultipart(t, "file", "x.png", makePNG(t, 50, 50))

	req := httptest.NewRequest("POST", "/admin/api/media", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	json.NewDecoder(w.Body).Decode(&got)
	if got["mime"] != "image/png" {
		t.Errorf("mime=%v", got["mime"])
	}
}

func TestUploadMedia_RejectsNonImage(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	body, ct := buildMultipart(t, "file", "x.txt", []byte("plain text not an image"))
	req := httptest.NewRequest("POST", "/admin/api/media", body)
	req.Header.Set("Content-Type", ct)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestUploadMedia_MissingField(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	body, ct := buildMultipart(t, "wrong", "x.png", makePNG(t, 10, 10))
	req := httptest.NewRequest("POST", "/admin/api/media", body)
	req.Header.Set("Content-Type", ct)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("code=%d body=%s", w.Code, w.Body.String())
	}
}

// --- SPA fallback ---

func TestSPA_ServesIndexForUnknown(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "GET", "/admin/some/spa/route", nil, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `data-app="mizu"`) {
		t.Errorf("SPA fallback didn't return index.html: %s", w.Body.String())
	}
}

func TestSPA_ServesAsset(t *testing.T) {
	h := newHarness(t)
	w := h.do(t, "GET", "/admin/assets/app.js", nil, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "console.log") {
		t.Errorf("asset body missing: %s", w.Body.String())
	}
}

func TestSPA_OverrideOnDiskWins(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"),
		[]byte(`<html data-source="disk"></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.cfg.Paths.AdminDist = dir
	w := h.do(t, "GET", "/admin/", nil, nil)
	if !strings.Contains(w.Body.String(), `data-source="disk"`) {
		t.Errorf("disk override didn't win: %s", w.Body.String())
	}
}

func TestSPA_PlaceholderWhenNoAdmin(t *testing.T) {
	h := newHarness(t)
	h.srv.dist = nil
	h.cfg.Paths.AdminDist = ""
	w := h.do(t, "GET", "/admin/", nil, nil)
	if w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mizu admin") {
		t.Errorf("placeholder missing: %s", w.Body.String())
	}
}

// --- helpers ---

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{0, 200, 0, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildMultipart(t *testing.T, field, filename string, data []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(data)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestMentions_ListReturnsVerifiedNewestFirst(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	now := nowUTC()
	must := func(m webmention.Mention) {
		if err := h.wmStr.Upsert(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	must(webmention.Mention{
		Source: "https://other.test/old", Target: "https://example.test/a",
		Status:     webmention.StatusVerified,
		ReceivedAt: now.Add(-2 * time.Hour), VerifiedAt: now.Add(-2 * time.Hour),
	})
	must(webmention.Mention{
		Source: "https://other.test/new", Target: "https://example.test/b",
		Status:     webmention.StatusVerified,
		ReceivedAt: now.Add(-1 * time.Hour), VerifiedAt: now.Add(-1 * time.Hour),
	})
	// Pending entry should not appear.
	must(webmention.Mention{
		Source: "https://other.test/pending", Target: "https://example.test/c",
		Status: webmention.StatusPending, ReceivedAt: now,
	})

	w := h.do(t, "GET", "/admin/api/mentions", nil, c)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2 (only verified)", len(got))
	}
	if got[0]["source"] != "https://other.test/new" {
		t.Errorf("got[0].source=%v, want newest first", got[0]["source"])
	}
	if got[0]["verified_at"] == "" || got[0]["verified_at"] == nil {
		t.Errorf("verified_at missing on verified row: %v", got[0])
	}
	if got[0]["source_host"] != "other.test" {
		t.Errorf("source_host=%v, want other.test", got[0]["source_host"])
	}
	if got[0]["target_path"] != "/b" {
		t.Errorf("target_path=%v, want /b", got[0]["target_path"])
	}
}

func TestMentions_ResolvesTargetTitleFromPostStore(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	// Create a real post so the URL path resolves to a known title.
	p, err := h.posts.Create("Hello World", "body text", nil)
	if err != nil {
		t.Fatal(err)
	}
	target := "https://example.test" + p.Path()
	if err := h.wmStr.Upsert(context.Background(), webmention.Mention{
		Source: "https://other.test/post", Target: target,
		Status:     webmention.StatusVerified,
		ReceivedAt: nowUTC(), VerifiedAt: nowUTC(),
	}); err != nil {
		t.Fatal(err)
	}
	w := h.do(t, "GET", "/admin/api/mentions", nil, c)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	if got[0]["target_title"] != "Hello World" {
		t.Errorf("target_title=%v, want Hello World", got[0]["target_title"])
	}
}

func TestMentions_TargetTitleAbsentWhenPostUnknown(t *testing.T) {
	h := newHarness(t)
	c := h.login(t)
	if err := h.wmStr.Upsert(context.Background(), webmention.Mention{
		Source: "https://other.test/post", Target: "https://example.test/2026/01/01/missing",
		Status:     webmention.StatusVerified,
		ReceivedAt: nowUTC(), VerifiedAt: nowUTC(),
	}); err != nil {
		t.Fatal(err)
	}
	w := h.do(t, "GET", "/admin/api/mentions", nil, c)
	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got[0]["target_title"]; present {
		t.Errorf("target_title should be omitted when unresolved, got %v", got[0]["target_title"])
	}
	if got[0]["target_path"] != "/2026/01/01/missing" {
		t.Errorf("target_path=%v", got[0]["target_path"])
	}
}

func nowUTC() time.Time { return time.Now().UTC() }

func jsonNum(n int64) string { return strconv.FormatInt(n, 10) }
