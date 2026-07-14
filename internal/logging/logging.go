package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"amdl/internal/config"

	"gopkg.in/natefinch/lumberjack.v2"
)

type System struct {
	Logger *slog.Logger
	Store  *Store
	closer io.Closer
	level  slog.LevelVar
}

func New(cfg config.LoggingConfig) (*System, error) {
	level, err := parseLevel(cfg.Level)
	if err != nil {
		return nil, err
	}
	var outputs []io.Writer
	if cfg.Console {
		outputs = append(outputs, os.Stdout)
	}
	var closer io.Closer
	if cfg.FileEnabled {
		if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o755); err != nil {
			return nil, fmt.Errorf("create log directory: %w", err)
		}
		probe, err := os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		if err := probe.Close(); err != nil {
			return nil, fmt.Errorf("close log file probe: %w", err)
		}
		file := &lumberjack.Logger{
			Filename: cfg.FilePath, MaxSize: cfg.MaxSizeMB, MaxBackups: cfg.MaxBackups,
			MaxAge: cfg.MaxAgeDays, Compress: cfg.Compress, LocalTime: true,
		}
		outputs = append(outputs, file)
		closer = file
	}
	output := io.Writer(io.Discard)
	if len(outputs) > 0 {
		output = io.MultiWriter(outputs...)
	}
	system := &System{Store: NewStore(cfg.BufferSize), closer: closer}
	system.level.Set(level)
	options := &slog.HandlerOptions{
		AddSource: cfg.IncludeSource,
		Level:     &system.level,
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if hasSensitiveGroup(groups) {
				attr.Value = slog.StringValue("[REDACTED]")
				return attr
			}
			return redactAttr(attr)
		},
	}
	var base slog.Handler
	if cfg.Format == "json" {
		base = slog.NewJSONHandler(output, options)
	} else {
		base = slog.NewTextHandler(output, options)
	}
	system.Logger = slog.New(&captureHandler{next: base, store: system.Store, includeSource: cfg.IncludeSource})
	return system, nil
}

func (s *System) SetLevel(value string) error {
	level, err := parseLevel(value)
	if err != nil {
		return err
	}
	s.level.Set(level)
	return nil
}

func (s *System) Close() error {
	if s == nil || s.closer == nil {
		return nil
	}
	return s.closer.Close()
}

func parseLevel(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported log level %q", value)
	}
}

type captureHandler struct {
	next          slog.Handler
	store         *Store
	attrs         []boundAttr
	groups        []string
	includeSource bool
}

type boundAttr struct {
	attr   slog.Attr
	groups []string
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *captureHandler) Handle(ctx context.Context, record slog.Record) error {
	attrs := map[string]any{}
	for _, bound := range h.attrs {
		addAttr(attrs, bound.groups, redactCaptured(bound.groups, bound.attr))
	}
	record.Attrs(func(attr slog.Attr) bool {
		addAttr(attrs, h.groups, redactCaptured(h.groups, attr))
		return true
	})
	entry := Entry{Time: record.Time, Level: levelName(record.Level), Message: record.Message, Attributes: attrs}
	if h.includeSource && record.PC != 0 {
		frame, _ := runtime.CallersFrames([]uintptr{record.PC}).Next()
		entry.Source = fmt.Sprintf("%s:%d", frame.File, frame.Line)
	}
	h.store.append(entry)
	return h.next.Handle(ctx, record)
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.next = h.next.WithAttrs(attrs)
	clone.attrs = append([]boundAttr(nil), h.attrs...)
	for _, attr := range attrs {
		clone.attrs = append(clone.attrs, boundAttr{attr: attr, groups: append([]string(nil), h.groups...)})
	}
	return &clone
}

func (h *captureHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.next = h.next.WithGroup(name)
	clone.groups = append(append([]string(nil), h.groups...), name)
	return &clone
}

func levelName(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return "error"
	case level >= slog.LevelWarn:
		return "warn"
	case level >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

// redactCaptured mirrors the output handlers' ReplaceAttr on the capture
// path: an attribute recorded inside a sensitive group (WithGroup) is
// redacted wholesale, otherwise per-key redaction applies. Without the group
// check, the in-memory log API would return raw values that the console and
// file outputs already redact.
func redactCaptured(groups []string, attr slog.Attr) slog.Attr {
	if hasSensitiveGroup(groups) {
		attr.Value = slog.StringValue("[REDACTED]")
		return attr
	}
	return redactAttr(attr)
}

func redactAttr(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if sensitiveKey(attr.Key) {
		attr.Value = slog.StringValue("[REDACTED]")
		return attr
	}
	if attr.Value.Kind() == slog.KindGroup {
		children := attr.Value.Group()
		for i := range children {
			children[i] = redactAttr(children[i])
		}
		attr.Value = slog.GroupValue(children...)
	} else if attr.Value.Kind() == slog.KindAny {
		attr.Value = slog.AnyValue(sanitizeAny(attr.Value.Any()))
	}
	return attr
}

func sanitizeAny(value any) any {
	if _, ok := value.(error); ok {
		return value
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return value
	}
	return sanitizeValue(normalized)
}

func sanitizeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if sensitiveKey(key) {
				typed[key] = "[REDACTED]"
			} else {
				typed[key] = sanitizeValue(child)
			}
		}
	case []any:
		for i := range typed {
			typed[i] = sanitizeValue(typed[i])
		}
	}
	return value
}

func sensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, part := range []string{"password", "passwd", "authorization", "cookie", "secret", "private_key", "token", "api_key", "bearer", "two_step_code"} {
		if normalized == part || strings.HasSuffix(normalized, "_"+part) {
			return true
		}
	}
	return false
}

func hasSensitiveGroup(groups []string) bool {
	for _, group := range groups {
		if sensitiveKey(group) {
			return true
		}
	}
	return false
}

func addAttr(root map[string]any, groups []string, attr slog.Attr) {
	if attr.Equal(slog.Attr{}) {
		return
	}
	target := root
	for _, group := range groups {
		next, _ := target[group].(map[string]any)
		if next == nil {
			next = map[string]any{}
			target[group] = next
		}
		target = next
	}
	if attr.Value.Kind() == slog.KindGroup {
		group := map[string]any{}
		for _, child := range attr.Value.Group() {
			addAttr(group, nil, child)
		}
		if attr.Key == "" {
			for key, value := range group {
				target[key] = value
			}
		} else {
			target[attr.Key] = group
		}
		return
	}
	target[attr.Key] = valueAny(attr.Value)
}

func valueAny(value slog.Value) any {
	switch value.Kind() {
	case slog.KindAny:
		if err, ok := value.Any().(error); ok {
			return err.Error()
		}
		raw, err := json.Marshal(value.Any())
		if err != nil {
			return fmt.Sprint(value.Any())
		}
		var normalized any
		if json.Unmarshal(raw, &normalized) == nil {
			return normalized
		}
		return fmt.Sprint(value.Any())
	case slog.KindBool:
		return value.Bool()
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindString:
		return value.String()
	case slog.KindTime:
		return value.Time().Format(time.RFC3339Nano)
	case slog.KindUint64:
		return value.Uint64()
	default:
		return value.String()
	}
}

type contextKey struct{}

func NewContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, logger)
}

func FromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if logger, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && logger != nil {
		return logger
	}
	if fallback != nil {
		return fallback
	}
	return slog.Default()
}
