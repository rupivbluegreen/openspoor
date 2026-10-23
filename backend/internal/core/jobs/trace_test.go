package jobs

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestJobSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	_ = safeRun(context.Background(), func(context.Context, Job) error { return nil }, Job{Kind: "projection.apply"})
	_ = safeRun(context.Background(), func(context.Context, Job) error { return errors.New("hub down") }, Job{Kind: "connector.submit", Attempts: 2})
	_ = safeRun(context.Background(), func(context.Context, Job) error { panic("boom") }, Job{Kind: "webhook.deliver"})

	spans := rec.Ended()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3", len(spans))
	}
	want := []struct {
		name string
		code codes.Code
	}{{"job projection.apply", codes.Unset}, {"job connector.submit", codes.Error}, {"job webhook.deliver", codes.Error}}
	for i, w := range want {
		if spans[i].Name() != w.name || spans[i].Status().Code != w.code {
			t.Errorf("span %d = %q %v, want %q %v", i, spans[i].Name(), spans[i].Status().Code, w.name, w.code)
		}
	}
}
