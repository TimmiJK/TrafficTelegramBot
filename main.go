package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

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
		PollInterval: 60 * time.Minute,
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

func syncClients(ctx context.Context, db *sql.DB, traffic map[string]Traffic) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		"INSERT INTO clients (user_key) VALUES ($1) ON CONFLICT (user_key) DO NOTHING")
	if err != nil {
		return err
	}
	defer stmt.Close()
	return nil
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

	if err := syncClients(ctx, pgDB, currentTraffic); err != nil {
		logger.Error("Failed to sync clients", "error", err)
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
		year, month, _ := now.Date()

		currentMonth30 := time.Date(year, month, 30, 0, 0, 0, 0, loc)

		if now.Day() < 30 {
			currentMonth30 = currentMonth30.AddDate(0, -1, 0)
		}

		start := currentMonth30.AddDate(0, -offset, 0)
		end := start.AddDate(0, 1, 0)

		if offset == 0 {
			end = now
		}
		return start.Unix(), end.Unix()
	}

	return 0, 0
}

func getUsageForPeriod(ctx context.Context, db *sql.DB, userKey string, startTS, endTS int64) (int64, int64, error) {
	var up, down int64
	err := db.QueryRowContext(
		ctx,
		`
		SELECT COALESCE(SUM(up), 0), COALESCE(SUM(down), 0)
		FROM usage
		WHERE user_key = $1
		  AND ts >= $2
		  AND ts < $3
		`,
		userKey,
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

func getBoundUser(ctx context.Context, db *sql.DB, chatID int64) (string, error) {
	var userKey string
	err := db.QueryRowContext(ctx,
		"SELECT user_key FROM bindings WHERE tg_chat_id = $1 LIMIT 1",
		chatID,
	).Scan(&userKey)
	return userKey, err
}

func sendPeriodReport(ctx context.Context, bot *tgbotapi.BotAPI, pgDB *sql.DB, chatID int64, period string, offset int, logger *slog.Logger) {
	userKey, err := getBoundUser(ctx, pgDB, chatID)
	if err != nil {
		msg := tgbotapi.NewMessage(chatID, "Сначала привяжите пользователя: /bind email@example.com")
		bot.Send(msg)
		return
	}

	startTS, endTS := periodBounds(period, offset)
	up, down, err := getUsageForPeriod(ctx, pgDB, userKey, startTS, endTS)
	if err != nil {
		logger.Error("Failed to get usage", "user", userKey, "period", period, "error", err)
		return
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

func isAdmin(chatID int64) (bool, error) {
	adminID := os.Getenv("ADMIN_CHAT_ID")
	adminIDInt64, err := strconv.ParseInt(adminID, 10, 64)
	if err != nil {
		return false, err
	}
	if adminIDInt64 == chatID {
		return true, nil
	}
	return false, nil
}

func handleBotCommands(ctx context.Context, bot *tgbotapi.BotAPI, pgDB *sql.DB, logger *slog.Logger) {
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
					"/bind <email> - привязать пользователя 3x-ui (Admin only)\n"+
					"/usage - статистика за день, неделю и месяц\n"+
					"/today /week /month - текущие периоды\n"+
					"/yesterday /prevweek /prevmonth - прошлые периоды")
			bot.Send(msg)

		case "usage":
			userKey, err := getBoundUser(ctx, pgDB, chatID)
			if err != nil {
				msg := tgbotapi.NewMessage(chatID, "Сначала привяжите пользователя: /bind email@example.com")
				bot.Send(msg)
				continue
			}

			report, err := formatReport(ctx, pgDB, userKey)
			if err != nil {
				logger.Error("Failed to format report", "error", err)
				continue
			}

			msg := tgbotapi.NewMessage(chatID, report)
			bot.Send(msg)

		case "bind":
			isAdmin, err := isAdmin(chatID)
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

			userData := strings.Split(strings.Trim(args, " "), " ")
			if len(userData) < 2 {
				msg := tgbotapi.NewMessage(chatID, "Нужно передать два аргумента chatID и email")
				bot.Send(msg)
				continue
			}
			bindChatID := userData[0]
			email := userData[1]

			_, err = pgDB.ExecContext(
				ctx,
				`
				INSERT INTO bindings (tg_chat_id, user_key, tz) 
				VALUES ($1, $2, $3) 
				ON CONFLICT (tg_chat_id, user_key) DO UPDATE SET tz = $3
				`,
				bindChatID,
				email,
				"UTC",
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
			isAdmin, err := isAdmin(chatID)
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

			userData := strings.Split(strings.Trim(args, " "), " ")
			if len(userData) < 2 {
				msg := tgbotapi.NewMessage(chatID, "Нужно передать два аргумента chatID и email")
				bot.Send(msg)
				continue
			}
			bindChatID := userData[0]
			email := userData[1]

			_, err = pgDB.ExecContext(ctx, `DELETE FROM bindings WHERE tg_chat_id = $1 AND user_key = $2`, bindChatID, email)

			if err != nil {
				msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Ошибка отвязки: %v", err))
				bot.Send(msg)
				continue
			}

			msg := tgbotapi.NewMessage(chatID, "✅ Пользователь отвязан")
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

	logger := slog.New(slog.NewJSONHandler(logWriter, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	logger.Info("Starting TrafficBot...")

	if err := godotenv.Load("./.env"); err != nil {
		logger.Warn("No .env file found, using environment variables")
	}

	cfg := loadConfig()

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

	handleBotCommands(context.Background(), bot, pgDB, logger)
}
