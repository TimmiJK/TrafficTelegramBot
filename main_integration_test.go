package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func setupPG(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — integration tests skipped")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open test postgres: %v", err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("ping test postgres: %v", err)
	}

	if _, err := db.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}

	applyMigrationFiles(t, db)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("failed to close db: %v", err)
		}
	}()
	return db
}

func applyMigrationFiles(t *testing.T, db *sql.DB) {
	t.Helper()

	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no .up.sql migrations found")
	}

	for _, n := range names {
		body, err := os.ReadFile(filepath.Join("migrations", n))
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply migration %s: %v", n, err)
		}
	}
}

func newTestXUIDB(t *testing.T, rows map[string][2]int64) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "xui.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open test sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE client_traffics (email TEXT, up INTEGER, down INTEGER)`); err != nil {
		t.Fatalf("create client_traffics: %v", err)
	}
	for email, v := range rows {
		if _, err := db.Exec(`INSERT INTO client_traffics (email, up, down) VALUES (?, ?, ?)`, email, v[0], v[1]); err != nil {
			t.Fatalf("seed %s: %v", email, err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type sentCapture struct {
	mu    sync.Mutex
	texts []string
}

func (c *sentCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.texts)
}

func (c *sentCapture) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.texts) == 0 {
		return ""
	}
	return c.texts[len(c.texts)-1]
}

func newFakeBot(t *testing.T) (*tgbotapi.BotAPI, *sentCapture) {
	t.Helper()
	cap := &sentCapture{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"id": 1, "is_bot": true, "first_name": "Test", "username": "test_bot",
				},
			})
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			r.ParseForm()
			text := r.PostFormValue("text")
			cap.mu.Lock()
			cap.texts = append(cap.texts, text)
			cap.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"result": map[string]any{
					"message_id": 1, "date": 1,
					"chat": map[string]any{"id": 42, "type": "private"},
					"text": text,
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		}
	}))
	t.Cleanup(srv.Close)

	bot, err := tgbotapi.NewBotAPIWithClient("123:TEST", srv.URL+"/bot%s/%s", srv.Client())
	if err != nil {
		t.Fatalf("create fake bot: %v", err)
	}
	return bot, cap
}

func TestStateAndUsageCycle(t *testing.T) {
	pg := setupPG(t)
	ctx := context.Background()

	_, _, exists, err := getLastState(ctx, pg, "testuser_mob")
	if err != nil || exists {
		t.Fatalf("getLastState on empty table = (%v, %v), want (nil, false)", err, exists)
	}

	if err := updateState(ctx, pg, "testuser_mob", 100, 200); err != nil {
		t.Fatalf("updateState: %v", err)
	}
	lastUp, lastDown, exists, err := getLastState(ctx, pg, "testuser_mob")
	if err != nil || !exists || lastUp != 100 || lastDown != 200 {
		t.Fatalf("getLastState = (%d, %d, %v, %v), want (100, 200, true, nil)", lastUp, lastDown, exists, err)
	}

	if err := insertUsage(ctx, pg, "testuser_mob", 0, 0); err != nil {
		t.Fatalf("insertUsage(0,0): %v", err)
	}

	if err := insertUsage(ctx, pg, "testuser_mob", 5, 7); err != nil {
		t.Fatalf("insertUsage: %v", err)
	}

	up, down, err := getUsageForPeriod(ctx, pg, "testuser", 0, time.Now().Unix()+60)
	if err != nil {
		t.Fatalf("getUsageForPeriod: %v", err)
	}
	if up != 5 || down != 7 {
		t.Fatalf("group usage = (%d, %d), want (5, 7)", up, down)
	}
}

func TestGroupsBindingsAndFK(t *testing.T) {
	pg := setupPG(t)
	ctx := context.Background()

	// группы строятся из state
	updateState(ctx, pg, "testuser_mob", 1, 1)
	updateState(ctx, pg, "granny", 1, 1)
	if err := syncGroups(ctx, pg); err != nil {
		t.Fatalf("syncGroups: %v", err)
	}

	var groups []string
	rows, err := pg.Query(`SELECT group_key FROM groups ORDER BY group_key`)
	if err != nil {
		t.Fatalf("select groups: %v", err)
	}
	for rows.Next() {
		var g string
		rows.Scan(&g)
		groups = append(groups, g)
	}
	rows.Close()
	if len(groups) != 2 || groups[0] != "granny" || groups[1] != "testuser" {
		t.Fatalf("groups = %v, want [granny testuser]", groups)
	}

	if _, err := pg.Exec(`INSERT INTO bindings (tg_chat_id, user_key) VALUES (1, 'testuser')`); err != nil {
		t.Fatalf("bind known group: %v", err)
	}
	if _, err := pg.Exec(`INSERT INTO bindings (tg_chat_id, user_key) VALUES (1, 'random')`); err == nil {
		t.Fatal("FK must reject unknown group")
	}

	keys, err := getBoundUsers(ctx, pg, 1)
	if err != nil || len(keys) != 1 || keys[0] != "testuser" {
		t.Fatalf("getBoundUsers = (%v, %v), want ([testuser], nil)", keys, err)
	}
}

func TestGetAllGroupsUsage(t *testing.T) {
	pg := setupPG(t)
	ctx := context.Background()

	updateState(ctx, pg, "testuser_mob", 1, 1)
	updateState(ctx, pg, "testuser_pc", 1, 1)
	updateState(ctx, pg, "granny", 1, 1)
	syncGroups(ctx, pg)

	insertUsage(ctx, pg, "testuser_mob", 10, 10)
	insertUsage(ctx, pg, "testuser_pc", 5, 5)
	insertUsage(ctx, pg, "granny", 1, 1)

	rows, err := getAllGroupsUsage(ctx, pg, 0, time.Now().Unix()+60)
	if err != nil {
		t.Fatalf("getAllGroupsUsage: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	if rows[0].Group != "testuser" || rows[0].Up != 15 || rows[0].Down != 15 {
		t.Fatalf("rows[0] = %+v, want testuser 15/15", rows[0])
	}
	if rows[1].Group != "granny" || rows[1].Up != 1 {
		t.Fatalf("rows[1] = %+v, want granny 1", rows[1])
	}
}

func TestPollTrafficEndToEnd(t *testing.T) {
	pg := setupPG(t)
	xui := newTestXUIDB(t, map[string][2]int64{
		"testuser_mob": {100, 200},
		"testuser_pc":  {50, 60},
	})
	ctx := context.Background()
	logger := testLogger()

	pollTraffic(ctx, xui, pg, logger)
	up, down, _ := getTotalUsage(ctx, pg, 0, time.Now().Unix()+60)
	if up != 0 || down != 0 {
		t.Fatalf("after baseline: usage = (%d, %d), want (0, 0)", up, down)
	}

	xui.Exec(`UPDATE client_traffics SET up = up + 10, down = down + 20`)
	pollTraffic(ctx, xui, pg, logger)
	up, down, _ = getTotalUsage(ctx, pg, 0, time.Now().Unix()+60)
	if up != 20 || down != 40 {
		t.Fatalf("after growth: usage = (%d, %d), want (20, 40)", up, down)
	}

	xui.Exec(`UPDATE client_traffics SET up = 5, down = 250 WHERE email = 'testuser_mob'`)
	pollTraffic(ctx, xui, pg, logger)
	up, down, _ = getTotalUsage(ctx, pg, 0, time.Now().Unix()+60)
	if up != 25 || down != 290 {
		t.Fatalf("after reset: usage = (%d, %d), want (25, 290)", up, down)
	}
}

func TestFormatReport(t *testing.T) {
	pg := setupPG(t)
	ctx := context.Background()

	insertUsage(ctx, pg, "testuser_mob", 1024, 2048)

	report, err := formatReport(ctx, pg, "testuser")
	if err != nil {
		t.Fatalf("formatReport: %v", err)
	}
	for _, want := range []string{"testuser", "Сегодня", "1.0 KB", "2.0 KB"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
}

func TestLimitTrafficAlertSendsOncePerPeriod(t *testing.T) {
	pg := setupPG(t)
	ctx := context.Background()

	insertUsage(ctx, pg, "testuser_mob", 2<<30, 0)

	bot, cap := newFakeBot(t)
	limit := int64(1) << 30

	limitTrafficAlert(ctx, bot, 42, limit, pg, testLogger())
	if cap.count() != 1 {
		t.Fatalf("alert sent %d times, want 1", cap.count())
	}
	if !strings.Contains(cap.last(), "Превышен лимит") {
		t.Fatalf("unexpected alert text: %s", cap.last())
	}

	limitTrafficAlert(ctx, bot, 42, limit, pg, testLogger())
	if cap.count() != 1 {
		t.Fatalf("alert duplicated: %d messages", cap.count())
	}
}

func TestLimitTrafficAlertBelowLimit(t *testing.T) {
	pg := setupPG(t)
	ctx := context.Background()

	insertUsage(ctx, pg, "testuser_mob", 1<<20, 0)

	bot, cap := newFakeBot(t)
	limitTrafficAlert(ctx, bot, 42, int64(1)<<30, pg, testLogger())

	if cap.count() != 0 {
		t.Fatalf("alert sent below limit: %d messages", cap.count())
	}
}
