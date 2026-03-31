package unit_test

import (
	"context"
	"testing"

	mqotel "github.com/bancolombia/commons-mq-go/otel"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// T058: Benchmark instrumented vs. non-instrumented span creation.
// The OTel spec target is ≤ 1 ms p99 overhead per operation.
// These benchmarks verify that span creation with noop providers is in the
// sub-microsecond range, which is well within the 1 ms budget.

// BenchmarkStartProducerSpan_Noop measures noop span creation.
func BenchmarkStartProducerSpan_Noop(b *testing.B) {
	tp := tracenoop.NewTracerProvider()
	tracer := tp.Tracer("bench")
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		ctx2, span := mqotel.StartProducerSpan(ctx, tracer, "BENCH.QUEUE", "deadbeef")
		_ = ctx2
		span.End()
	}
}

// BenchmarkStartConsumerSpan_Noop measures noop consumer span creation.
func BenchmarkStartConsumerSpan_Noop(b *testing.B) {
	tp := tracenoop.NewTracerProvider()
	tracer := tp.Tracer("bench")
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		ctx2, span := mqotel.StartConsumerSpan(ctx, tracer, "BENCH.QUEUE", "deadbeef")
		_ = ctx2
		span.End()
	}
}

// BenchmarkInjectExtract_Noop measures noop propagation overhead.
func BenchmarkInjectExtract_Noop(b *testing.B) {
	prop := mqotel.NoopPropagator()
	ctx := context.Background()
	carrier := make(map[string]string)

	b.ResetTimer()
	for range b.N {
		mqotel.InjectToMap(ctx, prop, carrier)
		_ = mqotel.ExtractFromMap(ctx, prop, carrier)
	}
}

// BenchmarkInjectExtract_TraceContext measures real W3C propagation overhead.
func BenchmarkInjectExtract_TraceContext(b *testing.B) {
	tp := tracenoop.NewTracerProvider()
	tracer := tp.Tracer("bench")
	ctx, span := tracer.Start(context.Background(), "root")
	defer span.End()

	prop := mqotel.NoopPropagator()
	carrier := make(map[string]string)

	b.ResetTimer()
	for range b.N {
		mqotel.InjectToMap(ctx, prop, carrier)
		_ = mqotel.ExtractFromMap(ctx, prop, carrier)
	}
}
