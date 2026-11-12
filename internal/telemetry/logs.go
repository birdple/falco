// Logs por OTLP para los servicios Go.
//
// Zerolog escribe una línea JSON; `NewLogWriter` la parsea y la emite al
// LoggerProvider global. El `trace_id`/`span_id` los pone `NewTraceHook` desde
// el `context.Context` del evento (`log.With().Ctx(ctx)`), y el writer los
// convierte en un span context para `Emit`, que es de donde el SDK los toma.
//
// Sin contexto, el log sale igual: no se inventa una traza. Sin provider
// global, `global.GetLoggerProvider()` es un no-op y no cuesta nada.

package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	otellogglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/trace"
)

type traceHook struct{}

// Run añade los ids del span activo al evento. Zerolog llama al hook antes de
// serializar, así que los campos llegan al JSON y el writer los puede leer.
func (traceHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	ctx := e.GetCtx()
	if ctx == nil {
		return
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return
	}
	e.Str("trace_id", sc.TraceID().String())
	e.Str("span_id", sc.SpanID().String())
}

// NewTraceHook devuelve el hook que correlaciona logs con trazas.
func NewTraceHook() zerolog.Hook { return traceHook{} }

type logWriter struct{}

// NewLogWriter devuelve un writer que reenvía cada línea JSON de Zerolog por
// OTLP. Es seguro para usar en paralelo con el writer de stdout.
func NewLogWriter() io.Writer { return logWriter{} }

func (logWriter) Write(p []byte) (int, error) {
	var record map[string]any
	if err := json.Unmarshal(p, &record); err != nil {
		// Una línea que no es JSON (p. ej. el ConsoleWriter en dev) no puede
		// romper el logging: se ignora para OTLP y sigue a stdout.
		return len(p), nil
	}

	ctx := context.Background()
	if traceID, ok := record["trace_id"].(string); ok {
		if spanID, ok := record["span_id"].(string); ok {
			if tid, err := trace.TraceIDFromHex(traceID); err == nil {
				if sid, err := trace.SpanIDFromHex(spanID); err == nil {
					sc := trace.NewSpanContext(trace.SpanContextConfig{
						TraceID:    tid,
						SpanID:     sid,
						TraceFlags: trace.FlagsSampled,
					})
					ctx = trace.ContextWithSpanContext(ctx, sc)
				}
			}
		}
	}

	var rec otellog.Record
	rec.SetTimestamp(time.Now())
	severity, severityText := severityFor(record["level"])
	rec.SetSeverity(severity)
	rec.SetSeverityText(severityText)

	message, _ := record["message"].(string)
	if message == "" {
		message, _ = record["msg"].(string)
	}
	rec.SetBody(attribute.StringValue(message))

	for key, value := range record {
		switch key {
		case "level", "time", "message", "msg", "trace_id", "span_id", "pid", "hostname":
			continue
		}
		switch v := value.(type) {
		case string:
			rec.AddAttributes(attribute.String(key, v))
		case float64:
			rec.AddAttributes(attribute.Int64(key, int64(v)))
		case bool:
			rec.AddAttributes(attribute.Bool(key, v))
		default:
			encoded, err := json.Marshal(v)
			if err == nil {
				rec.AddAttributes(attribute.String(key, string(encoded)))
			}
		}
	}

	otellogglobal.GetLoggerProvider().Logger("zerolog").Emit(ctx, rec)
	return len(p), nil
}

// severityFor entiende el nivel string de Zerolog y, por si acaso, el numérico
// de Pino. Un valor desconocido cae a INFO.
func severityFor(level any) (otellog.Severity, string) {
	switch v := level.(type) {
	case string:
		switch strings.ToLower(v) {
		case "trace":
			return otellog.SeverityTrace, "TRACE"
		case "debug":
			return otellog.SeverityDebug, "DEBUG"
		case "warn", "warning":
			return otellog.SeverityWarn, "WARN"
		case "error":
			return otellog.SeverityError, "ERROR"
		case "fatal", "panic":
			return otellog.SeverityFatal, "FATAL"
		default:
			return otellog.SeverityInfo, "INFO"
		}
	case float64:
		switch {
		case v <= 10:
			return otellog.SeverityTrace, "TRACE"
		case v <= 20:
			return otellog.SeverityDebug, "DEBUG"
		case v <= 30:
			return otellog.SeverityInfo, "INFO"
		case v <= 40:
			return otellog.SeverityWarn, "WARN"
		case v <= 50:
			return otellog.SeverityError, "ERROR"
		default:
			return otellog.SeverityFatal, "FATAL"
		}
	default:
		return otellog.SeverityInfo, "INFO"
	}
}
