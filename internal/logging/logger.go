package logging

import (
	"log/slog"
	"os"
)

func Init(workerID string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler).With("worker_id", workerID)
	slog.SetDefault(logger)
	return logger
}
