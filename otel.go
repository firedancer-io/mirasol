package main

import (
	"io"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
)

// newConsoleExporter returns a console exporter.
func newConsoleExporter(w io.Writer) (trace.SpanExporter, error) {
	return stdouttrace.New(
		stdouttrace.WithWriter(w),
		// Use human readable output.
		stdouttrace.WithPrettyPrint(),
	)
}

// newResource returns a resource describing this application.
func newResource() *resource.Resource {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "undefined"
	}
	r, _ := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String("mirasol"),
			semconv.ServiceVersionKey.String(version),
			attribute.String("hostname", hostname),
			attribute.Int("pid", os.Getpid()),
		),
	)
	return r
}
