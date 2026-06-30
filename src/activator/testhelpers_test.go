package activator

import (
	"log/slog"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
)

func testLogger() *slog.Logger {
	cfg := pkglog.DefaultConfig()
	cfg.Level = "error" // quiet tests
	return pkglog.New(cfg)
}
