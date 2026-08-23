package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	"github.com/lib/pq"
	"gopkg.in/natefinch/lumberjack.v2"
	_ "modernc.org/sqlite"
)

type Config struct {
	DBHost       string
	DBPort       string
	DBUser       string
	DBPassword   string
	DBName       string
	XUIPath      string
	BotToken     string
	PollInterval time.Duration
	BillingDay   int
}

func loadConfig() Config {
	return Config{
		DBHost:       os.Getenv("DB_HOST"),
		DBPort:       os.Getenv("DB_PORT"),
		DBUser:       os.Getenv("DB_USER"),
		DBPassword:   os.Getenv("DB_PASSWORD"),
		DBName:       os.Getenv("DB_NAME"),
		XUIPath:      os.Getenv("XUI_DB_PATH"),
		BotToken:     os.Getenv("BOT_TOKEN"),
		PollInterval: envDurationSec("POLL_INTERVAL", 600),
		BillingDay:   envInt("BILLING_DAY", 29),
	}
}

func NewPostgresDB(cfg Config) (*sql.DB, error) {
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		cfg.DBHost, cfg.DBPort, cfg.DBUser, cfg.DBPassword, cfg.DBName)

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database connection: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)

	return db, nil
}

func openXUIDB(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro", path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA busy_timeout = 5000;"); err != nil {
		return nil, err
	}

	return db, nil
}

func envInt(key string, defaultVal int) int {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return val
		}
	}
	return defaultVal
}

func envDurationSec(key string, defaultVal int) time.Duration {
	if valStr := os.Getenv(key); valStr != "" {
		if val, err := strconv.Atoi(valStr); err == nil {
			return time.Duration(val) * time.Second
		}
	}
	return time.Duration(defaultVal) * time.Second
}

type Traffic struct {
	Up   int64
	Down int64
}

func fetchAllTraffic(ctx context.Context, xuiDB *sql.DB) (map[string]Traffic, error) {
	rows, err := xuiDB.QueryContext(ctx, "SELECT email, COALESCE(SUM(up),0), COALESCE(SUM(down),0) FROM client_traffics GROUP BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]Traffic)
	for rows.Next() {
		var email string
		var up, down int64
		err := rows.Scan(&email, &up, &down)
		if err != nil {
			return nil, err
		}

		result[strings.ToLower(email)] = Traffic{Up: up, Down: down}
	}
	return result, nil
}

func getLastState(ctx context.Context, db *sql.DB, userKey string) (int64, int64, bool, error) {
	var lastUp, lastDown int64
	err := db.QueryRowContext(
		ctx,
		"SELECT last_up, last_down FROM state WHERE user_key = $1",
		userKey,
	).Scan(&lastUp, &lastDown)

	if err == sql.ErrNoRows {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}

	return lastUp, lastDown, true, nil
}

func updateState(ctx context.Context, db *sql.DB, userKey string, up, down int64) error {
	res, err := db.ExecContext(
		ctx,
		`
		INSERT INTO state (user_key, last_up, last_down, last_ts)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_key) 
		DO UPDATE SET last_up = $2, last_down = $3, last_ts = $4
		`,
		userKey,
		up,
		down,
		time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("db insert/update error: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func insertUsage(ctx context.Context, db *sql.DB, userKey string, up, down int64) error {
	if up == 0 && down == 0 {
		return nil
	}

	res, err := db.ExecContext(
		ctx,
		`
		INSERT INTO usage (user_key, ts, up, down)
		VALUES ($1, $2, $3, $4)
		`,
		userKey,
		time.Now().Unix(),
		up,
		down,
	)

	if err != nil {
		return fmt.Errorf("db insert error: %w", err)
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func syncGroups(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		INSERT INTO groups (group_key)
		SELECT DISTINCT split_part(user_key, '_', 1)
		FROM state
		ON CONFLICT (group_key) DO NOTHING`)
	return err
}

func pollTraffic(ctx context.Context, xuiDB, pgDB *sql.DB, logger *slog.Logger) {
	currentTraffic, err := fetchAllTraffic(ctx, xuiDB)
	if err != nil {
		logger.Error("Failed to fetch traffic from 3x-ui", "error", err)
		return
	}

	if len(currentTraffic) == 0 {
		logger.Warn("No traffic data from 3x-ui")
		return
	}

	for userKey, current := range currentTraffic {
		lastUp, lastDown, exists, err := getLastState(ctx, pgDB, userKey)
		if err != nil {
			logger.Error("Failed to get last state", "user", userKey, "error", err)
			continue
		}

		if !exists {
			if err := updateState(ctx, pgDB, userKey, current.Up, current.Down); err != nil {
				logger.Error("Failed to save baseline", "user", userKey, "error", err)
			}
			logger.Info("Baseline saved for new user", "user", userKey, "up", current.Up, "down", current.Down)
			continue
		}

		upDelta := current.Up - lastUp
		downDelta := current.Down - lastDown

		if upDelta < 0 || downDelta < 0 {
			logger.Warn("Traffic counter reset detected", "user", userKey, "lastUp", lastUp, "lastDown", lastDown, "currentUp", current.Up, "currentDown", current.Down)

			upDelta = current.Up
			downDelta = current.Down
		}

		if upDelta > 0 || downDelta > 0 {
			if err := insertUsage(ctx, pgDB, userKey, upDelta, downDelta); err != nil {
				logger.Error("Failed to insert usage", "user", userKey, "error", err)
				continue
			}
		}

		if err := updateState(ctx, pgDB, userKey, current.Up, current.Down); err != nil {
			logger.Error("Failed to update state", "user", userKey, "error", err)
		}
	}

	if err := syncGroups(ctx, pgDB); err != nil {
		logger.Error("Failed to sync groups", "error", err)
	}
}

var billingDay int

func daysInMonth(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func billingStart(y int, m time.Month, day int, loc *time.Location) time.Time {
	if d := daysInMonth(y, m); day > d {
		day = d
	}
	return time.Date(y, m, day, 0, 0, 0, 0, loc)
}

func periodBounds(period string, offset int) (int64, int64) {
	now := time.Now()
	loc := now.Location()

	switch period {
	case "day":
		year, month, day := now.Date()
		start := time.Date(year, month, day, 0, 0, 0, 0, loc).AddDate(0, 0, -offset)
		end := start.AddDate(0, 0, 1)
		if offset == 0 {
			end = now
		}
		return start.Unix(), end.Unix()

	case "week":
		offsetWeekday := (int(now.Weekday()) + 6) % 7
		year, month, day := now.Date()
		currentWeekStart := time.Date(year, month, day, 0, 0, 0, 0, loc).AddDate(0, 0, -offsetWeekday)

		start := currentWeekStart.AddDate(0, 0, -7*offset)
		end := start.AddDate(0, 0, 7)
		if offset == 0 {
			end = now
		}
		return start.Unix(), end.Unix()

	case "month":
		y, m, _ := now.Date()
		cur := billingStart(y, m, billingDay, loc)
		if now.Before(cur) {
			m--
			if m == 0 {
				m, y = 12, y-1
			}
			cur = billingStart(y, m, billingDay, loc)
		}
		sy, sm, _ := cur.Date()
		start := cur
		for i := 0; i < offset; i++ {
			sm--
			if sm == 0 {
				sm, sy = 12, sy-1
			}
			start = billingStart(sy, sm, billingDay, loc)
		}
		ey, em, _ := start.Date()
		em++
		if em == 13 {
			em, ey = 1, ey+1
		}
		end := billingStart(ey, em, billingDay, loc)
		if offset == 0 {
			end = now
		}
		return start.Unix(), end.Unix()
	}

	return 0, 0
}

func getUsageForPeriod(ctx context.Context, db *sql.DB, groupKey string, startTS, endTS int64) (int64, int64, error) {
	var up, down int64
	err := db.QueryRowContext(
		ctx,
		`
		SELECT COALESCE(SUM(up), 0), COALESCE(SUM(down), 0)
		FROM usage
		WHERE split_part(user_key, '_', 1) = $1
		  AND ts >= $2
		  AND ts < $3
		`,
		groupKey,
		startTS,
		endTS,
	).Scan(
		&up,
		&down,
	)

	if err != nil && err != sql.ErrNoRows {
		return 0, 0, err
	}

	return up, down, nil
}

func ConvertBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(value)/float64(div), "KMGTPE"[exp])
}

func formatReport(ctx context.Context, db *sql.DB, userKey string) (string, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👤 Пользователь: %s\n\n", userKey))

	periods := []struct {
		code string
		name string
	}{
		{"day", "Сегодня"},
		{"week", "Эта неделя"},
		{"month", "Этот месяц"},
	}

	for _, p := range periods {
		startTS, endTS := periodBounds(p.code, 0)
		up, down, err := getUsageForPeriod(ctx, db, userKey, startTS, endTS)
		if err != nil {
			return "", err
		}

		sb.WriteString(fmt.Sprintf("%s:\n  ↑ %s\n  ↓ %s\n  Σ %s\n\n",
			p.name, ConvertBytes(up), ConvertBytes(down), ConvertBytes(up+down)))
	}

	return strings.TrimSpace(sb.String()), nil
}

func getBoundUsers(ctx context.Context, db *sql.DB, chatID int64) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT user_key FROM bindings WHERE tg_chat_id = $1 ORDER BY user_key", chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func sendPeriodReport(ctx context.Context, bot *tgbotapi.BotAPI, pgDB *sql.DB, chatID int64, period string, offset int, logger *slog.Logger) {
	keys, err := getBoundUsers(ctx, pgDB, chatID)
	if err != nil {
		if err == sql.ErrNoRows {
			msg := tgbotapi.NewMessage(chatID, "Сначала привяжите пользователя: /bind email@example.com")
			bot.Send(msg)
			return
		}
		logger.Error("Failed to get bound user", "chatID", chatID, "error", err)
		msg := tgbotapi.NewMessage(chatID, "Произошла ошибка, попробуйте позже.")
		bot.Send(msg)
		return
	}

	if len(keys) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "В этом чате нет привязок. Попросите админа сделать /bind (@"+os.Getenv("ADMIN_USER_NAME")+")"))
		return
	}

	for _, userKey := range keys {
		startTS, endTS := periodBounds(period, offset)
		up, down, err := getUsageForPeriod(ctx, pgDB, userKey, startTS, endTS)
		if err != nil {
			logger.Error("Failed to get usage", "user", userKey, "period", period, "error", err)
			continue
		}

		name := map[string]string{
			"day":   "Сегодня",
			"week":  "Эта неделя",
			"month": "Этот месяц",
		}[period]

		if offset == 1 {
			name = map[string]string{
				"day":   "Вчера",
				"week":  "Прошлая неделя",
				"month": "Прошлый месяц",
			}[period]
		}

		text := fmt.Sprintf("👤 Пользователь: %s\nПериод: %s\n\n↑ %s\n↓ %s\nΣ %s",
			userKey, name, ConvertBytes(up), ConvertBytes(down), ConvertBytes(up+down))

		msg := tgbotapi.NewMessage(chatID, text)
		bot.Send(msg)
	}
}

var periodMap = map[string]struct {
	code   string
	offset int
	name   string
}{
	"today":     {"day", 0, "Сегодня"},
	"week":      {"week", 0, "Эта неделя"},
	"month":     {"month", 0, "Этот месяц"},
	"yesterday": {"day", 1, "Вчера"},
	"prevweek":  {"week", 1, "Прошлая неделя"},
	"prevmonth": {"month", 1, "Прошлый месяц"},
}

type groupUsage struct {
	Group string
	Up    int64
	Down  int64
}

func getAllGroupsUsage(ctx context.Context, db *sql.DB, startTS, endTS int64) ([]groupUsage, error) {
	rows, err := db.QueryContext(
		ctx,
		`
		SELECT g.group_key,
		       COALESCE(SUM(u.up), 0),
		       COALESCE(SUM(u.down), 0)
		FROM groups g
		LEFT JOIN usage u
		       ON split_part(u.user_key, '_', 1) = g.group_key
		      AND u.ts >= $1 AND u.ts < $2
		GROUP BY g.group_key
		ORDER BY (COALESCE(SUM(u.up), 0) + COALESCE(SUM(u.down), 0)) DESC, g.group_key
		`,
		startTS, endTS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []groupUsage
	for rows.Next() {
		var g groupUsage
		if err := rows.Scan(&g.Group, &g.Up, &g.Down); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func sendAllGroupsReport(ctx context.Context, bot *tgbotapi.BotAPI, pgDB *sql.DB, chatID int64, periodKey string, logger *slog.Logger) {
	p, ok := periodMap[periodKey]
	if !ok {
		bot.Send(tgbotapi.NewMessage(chatID, "Неизвестный период. Доступно: today, week, month, yesterday, prevweek, prevmonth"))
		return
	}

	startTS, endTS := periodBounds(p.code, p.offset)
	rows, err := getAllGroupsUsage(ctx, pgDB, startTS, endTS)
	if err != nil {
		logger.Error("Failed to get all groups usage", "error", err)
		bot.Send(tgbotapi.NewMessage(chatID, "Произошла ошибка, попробуйте позже."))
		return
	}
	if len(rows) == 0 {
		bot.Send(tgbotapi.NewMessage(chatID, "Нет данных за период"))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Трафик по пользователям\nПериод: %s\n\n", p.name))

	var totalUp, totalDown int64
	for _, r := range rows {
		totalUp += r.Up
		totalDown += r.Down
		sb.WriteString(fmt.Sprintf("%s: ↑ %s ↓ %s Σ %s\n",
			r.Group, ConvertBytes(r.Up), ConvertBytes(r.Down), ConvertBytes(r.Up+r.Down)))
	}
	sb.WriteString(fmt.Sprintf("\nИтого: ↑ %s ↓ %s Σ %s",
		ConvertBytes(totalUp), ConvertBytes(totalDown), ConvertBytes(totalUp+totalDown)))

	bot.Send(tgbotapi.NewMessage(chatID, sb.String()))
}

func isAdmin(adminID, chatID int64) (bool, error) {
	if adminID == chatID {
		return true, nil
	}
	return false, nil
}

func getTotalUsage(ctx context.Context, db *sql.DB, startTS, endTS int64) (int64, int64, error) {
	var up, down int64
	err := db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(up), 0), COALESCE(SUM(down), 0)
		FROM usage
		WHERE ts >= $1 AND ts < $2`,
		startTS, endTS,
	).Scan(&up, &down)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, err
	}
	return up, down, nil
}

func limitTrafficAlert(ctx context.Context, bot *tgbotapi.BotAPI, adminID, limitBytes int64, pgDB *sql.DB, logger *slog.Logger) {
	startTS, endTS := periodBounds("month", 0)

	totalUp, totalDown, err := getTotalUsage(ctx, pgDB, startTS, endTS)
	if err != nil {
		logger.Error("Failed to get total usage", "error", err)
		return
	}

	total := totalUp + totalDown
	if total <= limitBytes {
		return
	}

	var lastPeriod int64
	err = pgDB.QueryRowContext(ctx,
		`SELECT period_start FROM alert_state WHERE key = 'month_limit'`,
	).Scan(&lastPeriod)
	if err == nil && lastPeriod == startTS {
		return
	}
	if err != nil && err != sql.ErrNoRows {
		logger.Error("Failed to read alert_state", "error", err)
		return
	}

	percent := float64(total) / float64(limitBytes) * 100
	text := fmt.Sprintf(
		"🚨 Превышен лимит общего трафика!\n\n"+
			"Лимит: %s\n"+
			"Использовано: ↑ %s ↓ %s Σ %s (%.0f%%)\n"+
			"Период: текущий биллинг-месяц",
		ConvertBytes(limitBytes),
		ConvertBytes(totalUp), ConvertBytes(totalDown), ConvertBytes(total), percent,
	)

	if _, err := bot.Send(tgbotapi.NewMessage(adminID, text)); err != nil {
		logger.Error("Failed to send limit alert", "error", err)
		return
	}

	if _, err := pgDB.ExecContext(ctx, `
		INSERT INTO alert_state (key, period_start, last_sent_ts)
		VALUES ('month_limit', $1, $2)
		ON CONFLICT (key) DO UPDATE SET period_start = $1, last_sent_ts = $2`,
		startTS, time.Now().Unix(),
	); err != nil {
		logger.Error("Failed to save alert state", "error", err)
	}

	logger.Info("Limit alert sent", "total", total, "percent", percent)
}

func handleBotCommands(ctx context.Context, adminID int64, bot *tgbotapi.BotAPI, pgDB *sql.DB, logger *slog.Logger) {
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60

	for update := range bot.GetUpdatesChan(updateConfig) {
		if update.Message == nil || !update.Message.IsCommand() {
			continue
		}

		chatID := update.Message.Chat.ID

		switch update.Message.Command() {
		case "start":
			msg := tgbotapi.NewMessage(chatID,
				"Привет! Команды:\n"+
					"/bind chatID <email> - привязать пользователя 3x-ui (Admin only)\n"+
					"/unbind chatID <email> - отвязать пользователя 3x-ui (Admin only)\n"+
					"/all [период] - сводка по всем пользователям (Admin only)\n"+
					"/usage - статистика за день, неделю и месяц\n"+
					"/myID - узнать свой chatID\n"+
					"/today /week /month - текущие периоды\n"+
					"/yesterday /prevweek /prevmonth - прошлые периоды")
			bot.Send(msg)

		case "myID":
			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ваш chatID: %d", chatID))
			bot.Send(msg)

		case "all":
			isAdmin, err := isAdmin(adminID, chatID)
			if err != nil {
				logger.Error("Failed to check admin status", "error", err)
				continue
			}
			if !isAdmin {
				msg := tgbotapi.NewMessage(chatID, "❌ У вас нет прав для выполнения этой команды.")
				bot.Send(msg)
				continue
			}

			arg := strings.ToLower(strings.TrimSpace(update.Message.CommandArguments()))
			if arg == "" {
				arg = "month"
			}
			sendAllGroupsReport(ctx, bot, pgDB, chatID, arg, logger)

		case "usage":
			keys, err := getBoundUsers(ctx, pgDB, chatID)
			if err != nil {
				if err == sql.ErrNoRows {
					msg := tgbotapi.NewMessage(chatID, "Сначала привяжите пользователя: /bind email@example.com")
					bot.Send(msg)
					continue
				}
				logger.Error("Failed to get bound user", "chatID", chatID, "error", err)
				msg := tgbotapi.NewMessage(chatID, "Произошла ошибка, попробуйте позже.")
				bot.Send(msg)
			}

			if len(keys) == 0 {
				bot.Send(tgbotapi.NewMessage(chatID, "В этом чате нет привязок. Попросите админа сделать /bind (@"+os.Getenv("ADMIN_USER_NAME")+")"))
				continue
			}

			for _, userKey := range keys {
				report, err := formatReport(ctx, pgDB, userKey)
				if err != nil {
					logger.Error("Failed to format report", "user", userKey, "error", err)
					continue
				}
				bot.Send(tgbotapi.NewMessage(chatID, report))
			}

		case "bind":
			isAdmin, err := isAdmin(adminID, chatID)
			if err != nil {
				logger.Error("Failed to check admin status", "error", err)
				continue
			}
			if !isAdmin {
				msg := tgbotapi.NewMessage(chatID, "❌ У вас нет прав для выполнения этой команды.")
				bot.Send(msg)
				continue
			}

			args := update.Message.CommandArguments()
			if args == "" {
				msg := tgbotapi.NewMessage(chatID, "Использование: /bind chatID email@example.com")
				bot.Send(msg)
				continue
			}

			userData := strings.Fields(args)
			targetChat, err := strconv.ParseInt(userData[0], 10, 64)
			if err != nil {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Первый аргумент должен быть числом (chat ID)"))
				continue
			}
			email := strings.ToLower(strings.TrimSpace(userData[1]))

			_, err = pgDB.ExecContext(
				ctx,
				`
				INSERT INTO bindings (tg_chat_id, user_key)
				VALUES ($1, $2)
				ON CONFLICT (tg_chat_id, user_key) DO NOTHING
				`,
				targetChat,
				email,
			)

			if err != nil {
				var pqErr *pq.Error
				if errors.As(err, &pqErr) && strings.Contains(pqErr.Message, "violates foreign key constraint") {
					msg := tgbotapi.NewMessage(chatID, "❌ Пользователь не найден в системе 3x-ui")
					bot.Send(msg)
					continue
				}
				msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка привязки: %v", err))
				bot.Send(msg)
				continue
			}

			msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Привязан пользователь: %s", email))
			bot.Send(msg)

		case "unbind":
			isAdmin, err := isAdmin(adminID, chatID)
			if err != nil {
				logger.Error("Failed to check admin status", "error", err)
				continue
			}
			if !isAdmin {
				msg := tgbotapi.NewMessage(chatID, "❌ У вас нет прав для выполнения этой команды.")
				bot.Send(msg)
				continue
			}

			args := update.Message.CommandArguments()
			if args == "" {
				msg := tgbotapi.NewMessage(chatID, "Использование: /unbind chatID email@example.com")
				bot.Send(msg)
				continue
			}

			userData := strings.Fields(args)
			targetChat, err := strconv.ParseInt(userData[0], 10, 64)
			if err != nil {
				bot.Send(tgbotapi.NewMessage(chatID, "❌ Первый аргумент должен быть числом (chat ID)"))
				continue
			}
			email := strings.ToLower(strings.TrimSpace(userData[1]))

			res, err := pgDB.ExecContext(ctx, `DELETE FROM bindings WHERE tg_chat_id = $1 AND user_key = $2`, targetChat, email)

			if err != nil {
				msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка отвязки: %v", err))
				bot.Send(msg)
				continue
			}

			var msg tgbotapi.MessageConfig
			if n, _ := res.RowsAffected(); n == 0 {
				msg = tgbotapi.NewMessage(chatID, "Привязка не найдена")
			} else {
				msg = tgbotapi.NewMessage(chatID, "✅ Пользователь отвязан")
			}
			bot.Send(msg)

		case "today":
			sendPeriodReport(ctx, bot, pgDB, chatID, "day", 0, logger)

		case "week":
			sendPeriodReport(ctx, bot, pgDB, chatID, "week", 0, logger)

		case "month":
			sendPeriodReport(ctx, bot, pgDB, chatID, "month", 0, logger)

		case "yesterday":
			sendPeriodReport(ctx, bot, pgDB, chatID, "day", 1, logger)

		case "prevweek":
			sendPeriodReport(ctx, bot, pgDB, chatID, "week", 1, logger)

		case "prevmonth":
			sendPeriodReport(ctx, bot, pgDB, chatID, "month", 1, logger)

		default:
			msg := tgbotapi.NewMessage(chatID, "Неизвестная команда. Используйте /start для списка команд.")
			bot.Send(msg)
		}
	}
}

func main() {
	logWriter := &lumberjack.Logger{
		Filename:   "logs/traffic-bot.log",
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
		Compress:   true,
	}
	defer logWriter.Close()

	logger := slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stdout, logWriter), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	logger.Info("Starting TrafficBot...")

	if err := godotenv.Load("./.env"); err != nil {
		logger.Warn("No .env file found, using environment variables")
	}

	cfg := loadConfig()
	billingDay = cfg.BillingDay
	adminID, err := strconv.ParseInt(os.Getenv("ADMIN_CHAT_ID"), 10, 64)
	if err != nil {
		logger.Warn("ADMIN_CHAT_ID invalid, limit alerts disabled")
	}
	if adminID == 0 {
		logger.Warn("ADMIN_CHAT_ID not set: /bind and /unbind are disabled")
	}

	pgDB, err := NewPostgresDB(cfg)
	if err != nil {
		logger.Error("Failed to connect to PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer pgDB.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := pgDB.PingContext(ctx); err != nil {
		logger.Error("Failed to ping PostgreSQL", "error", err)
		os.Exit(1)
	}
	cancel()
	logger.Info("Connected to PostgreSQL")

	xuiDB, err := openXUIDB(cfg.XUIPath)
	if err != nil {
		logger.Error("Failed to connect to 3x-ui database", "error", err)
		os.Exit(1)
	}
	defer xuiDB.Close()

	if err := xuiDB.Ping(); err != nil {
		logger.Error("Failed to ping 3x-ui database", "error", err)
		os.Exit(1)
	}
	logger.Info("Connected to 3x-ui SQLite")

	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		logger.Error("Failed to create Telegram bot", "error", err)
		os.Exit(1)
	}
	logger.Info("Telegram bot authorized", "username", bot.Self.UserName)

	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()

		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			pollTraffic(ctx, xuiDB, pgDB, logger)
			cancel()
		}
	}()

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	pollTraffic(ctx, xuiDB, pgDB, logger)
	cancel()

	limitBytes := int64(envInt("TRAFFIC_LIMIT_GB", 2458)) << 30

	if adminID != 0 {
		go func() {
			check := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				limitTrafficAlert(ctx, bot, adminID, limitBytes, pgDB, logger)
				cancel()
			}
			check()
			ticker := time.NewTicker(30 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				check()
			}
		}()
	}

	handleBotCommands(context.Background(), adminID, bot, pgDB, logger)
}
