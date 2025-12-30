package logging

import (
	"log/slog"
	"os"
)

func Init(workerID string) *slog.Logger {
	var handler slog.Handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	handler = newRedactingHandler(handler)
	logger := slog.New(handler).With("worker_id", workerID)
	slog.SetDefault(logger)
	return logger
}
