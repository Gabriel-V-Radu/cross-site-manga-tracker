package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	Environment     string
	AppName         string
	Port            string
	LogLevel        slog.Level
	SQLitePath      string
	MigrationsPath  string
	SeedDefaultData bool
	PollingEnabled  bool
	PollingMinutes  int
	// PollingIdleMinutes is the minimum minutes between polls for trackers
	// that are not in "reading" status.
	PollingIdleMinutes int
	// WebDir holds the dashboard's templates and static assets; DataDir holds
	// the cover cache and the uploads. Both were spelled out relative to the
	// working directory at a dozen call sites, which is why the tests had to
	// chdir to the backend root before a handler would work at all. The
	// defaults are the layout the container runs with: the image copies web/
	// next to the binary under /app and the data volume mounts at /app/data.
	//
	// DataDir does NOT move the database: SQLitePath is its own setting and
	// keeps its own default, so a deployment that relocates DataDir has to set
	// SQLITE_PATH as well or the file stays where it was.
	WebDir  string
	DataDir string
}

// AssetsDir is the directory behind /assets.
func (c Config) AssetsDir() string { return filepath.Join(c.WebDir, "assets") }

// TemplatesGlob matches every dashboard template.
func (c Config) TemplatesGlob() string { return filepath.Join(c.WebDir, "templates", "*.html") }

// CoversDir is where resolved cover art is downloaded and served from. It has
// to stay the directory the /covers route is mounted on: the resolver mints
// file names there and hands them to the browser as hrefs.
func (c Config) CoversDir() string { return filepath.Join(c.DataDir, "covers") }

// UploadsDir is the directory behind /uploads.
func (c Config) UploadsDir() string { return filepath.Join(c.DataDir, "uploads") }

// SourceLogosDir holds the per-profile site logos the profile menu uploads.
// The public path they are stored under (/uploads/site-logos/...) is built
// from the /uploads mount, so the two must agree.
func (c Config) SourceLogosDir() string { return filepath.Join(c.UploadsDir(), "site-logos") }

func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Environment:        getEnv("APP_ENV", "development"),
		AppName:            getEnv("APP_NAME", "cross-site-tracker"),
		Port:               getEnvAsPort("APP_PORT", "8080"),
		SQLitePath:         getEnv("SQLITE_PATH", "./data/app.sqlite"),
		MigrationsPath:     getEnv("MIGRATIONS_PATH", "./migrations"),
		SeedDefaultData:    getEnvAsBool("SEED_DEFAULT_DATA", true),
		PollingEnabled:     getEnvAsBool("POLLING_ENABLED", true),
		PollingMinutes:     getEnvAsInt("POLLING_MINUTES", 30),
		PollingIdleMinutes: getEnvAsInt("POLLING_IDLE_MINUTES", 720),
		WebDir:             getEnv("WEB_DIR", "./web"),
		DataDir:            getEnv("DATA_DIR", "./data"),
	}

	if cfg.PollingMinutes <= 0 {
		cfg.PollingMinutes = 30
	}
	if cfg.PollingIdleMinutes <= 0 {
		cfg.PollingIdleMinutes = 720
	}

	cfg.LogLevel = parseLogLevel(getEnv("LOG_LEVEL", "INFO"))

	return cfg, nil
}

// parseLogLevel accepts any casing and never refuses to start. It used to
// return an error, which main turned into a non-zero exit — and with the
// compose file's restart policy, LOG_LEVEL=debug instead of DEBUG put the
// container into a crash loop over nothing. A log level is not worth refusing
// to run for; every other malformed setting warns and falls back, and this
// one now matches.
func parseLogLevel(raw string) slog.Level {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		warnBadEnv("LOG_LEVEL", raw, "INFO")
		return slog.LevelInfo
	}
}

func getEnv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

// A malformed value falls back to the default rather than refusing to start:
// this process runs headless on a Pi and a container that will not come up is
// worse than one running on defaults. It is logged loudly instead, because
// POLLING_MINUTES=3O silently polling every 30 minutes looks exactly like the
// setting having been applied.
func getEnvAsBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		warnBadEnv(key, value, fmt.Sprintf("%t", fallback))
		return fallback
	}
	return parsed
}

// getEnvAsPort is the same policy for the one setting that could still put the
// container into a restart loop: a port outside 1-65535 (or "8O80") passed
// Load, app.Listen failed, main exited 1, and compose restarted it forever.
func getEnvAsPort(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		warnBadEnv(key, value, fallback)
		return fallback
	}
	return strconv.Itoa(port)
}

func getEnvAsInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		warnBadEnv(key, value, strconv.Itoa(fallback))
		return fallback
	}
	return parsed
}

// warnBadEnv goes through the default logger: Load runs before main installs
// its own, so this is the only handler there is at that point.
func warnBadEnv(key string, value string, fallback string) {
	slog.Warn("ignoring malformed environment variable; using the default",
		"variable", key, "value", value, "default", fallback)
}
