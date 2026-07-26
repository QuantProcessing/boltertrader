package observability

import (
	"io"
	"regexp"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	safeLogTokenPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	safeCorrelationPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type SafeLogger struct {
	logger *zap.Logger
}

func NewSafeLogger(output io.Writer) *SafeLogger {
	if output == nil {
		output = io.Discard
	}
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(output), zapcore.InfoLevel)
	return &SafeLogger{logger: zap.New(core)}
}

func (logger *SafeLogger) Info(event, code, correlationID string) {
	logger.write(zap.InfoLevel, event, code, correlationID)
}

func (logger *SafeLogger) Error(event, code, correlationID string) {
	logger.write(zap.ErrorLevel, event, code, correlationID)
}

func (logger *SafeLogger) Sync() error {
	if logger == nil || logger.logger == nil {
		return nil
	}
	return logger.logger.Sync()
}

func (logger *SafeLogger) write(level zapcore.Level, event, code, correlationID string) {
	if logger == nil || logger.logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("event", safeLogToken(event)),
		zap.String("code", safeLogToken(code)),
	}
	if safeCorrelationPattern.MatchString(correlationID) {
		fields = append(fields, zap.String("correlation_id", correlationID))
	}
	if level == zap.ErrorLevel {
		logger.logger.Error("gateway_event", fields...)
		return
	}
	logger.logger.Info("gateway_event", fields...)
}

func safeLogToken(value string) string {
	if safeLogTokenPattern.MatchString(value) {
		return value
	}
	return "unknown"
}
