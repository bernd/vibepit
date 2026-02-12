package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func TestOTLPReceiver_Logs(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	receiver := NewOTLPReceiver(buf)

	t.Run("accepts valid log export", func(t *testing.T) {
		req := &collectorlogs.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{{
						Key:   "service.name",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
					}},
				},
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						Attributes: []*commonpb.KeyValue{
							{Key: "event.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "tool_result"}}},
							{Key: "tool_name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "Read"}}},
							{Key: "success", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "true"}}},
							{Key: "duration_ms", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "42"}}},
						},
					}},
				}},
			}},
		}
		body, err := proto.Marshal(req)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code)

		events := buf.Events()
		require.Len(t, events, 1)
		assert.Equal(t, "claude-code", events[0].Agent)
		assert.Equal(t, "tool_result", events[0].EventName)
		assert.Equal(t, "Read", events[0].Attrs["tool_name"])
	})

	t.Run("rejects oversized body", func(t *testing.T) {
		w := httptest.NewRecorder()
		bigBody := bytes.NewReader(make([]byte, 5*1024*1024))
		httpReq := httptest.NewRequest("POST", "/v1/logs", bigBody)
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	})

	t.Run("rejects non-POST", func(t *testing.T) {
		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("GET", "/v1/logs", nil)
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
	})

	t.Run("falls back to unknown agent when service.name missing", func(t *testing.T) {
		req := &collectorlogs.ExportLogsServiceRequest{
			ResourceLogs: []*logspb.ResourceLogs{{
				ScopeLogs: []*logspb.ScopeLogs{{
					LogRecords: []*logspb.LogRecord{{
						TimeUnixNano: uint64(time.Now().UnixNano()),
						Attributes: []*commonpb.KeyValue{
							{Key: "event.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "user_prompt"}}},
						},
					}},
				}},
			}},
		}
		body, _ := proto.Marshal(req)

		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("POST", "/v1/logs", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code)
		events := buf.Events()
		last := events[len(events)-1]
		assert.Equal(t, "unknown", last.Agent)
	})
}

func TestOTLPReceiver_Metrics(t *testing.T) {
	buf := NewTelemetryBuffer(100)
	receiver := NewOTLPReceiver(buf)

	t.Run("accepts valid metric export", func(t *testing.T) {
		req := &collectormetrics.ExportMetricsServiceRequest{
			ResourceMetrics: []*metricspb.ResourceMetrics{{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{{
						Key:   "service.name",
						Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "claude-code"}},
					}},
				},
				ScopeMetrics: []*metricspb.ScopeMetrics{{
					Metrics: []*metricspb.Metric{{
						Name: "claude_code.token.usage",
						Data: &metricspb.Metric_Sum{
							Sum: &metricspb.Sum{
								DataPoints: []*metricspb.NumberDataPoint{{
									Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: 1234},
									Attributes: []*commonpb.KeyValue{
										{Key: "type", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "input"}}},
									},
								}},
							},
						},
					}},
				}},
			}},
		}
		body, err := proto.Marshal(req)
		require.NoError(t, err)

		w := httptest.NewRecorder()
		httpReq := httptest.NewRequest("POST", "/v1/metrics", bytes.NewReader(body))
		httpReq.Header.Set("Content-Type", "application/x-protobuf")
		receiver.ServeHTTP(w, httpReq)

		assert.Equal(t, http.StatusOK, w.Code)

		metrics := buf.Metrics()
		require.Len(t, metrics, 1)
		assert.Equal(t, "claude_code.token.usage", metrics[0].Name)
		assert.Equal(t, "claude-code", metrics[0].Agent)
		assert.Equal(t, float64(1234), metrics[0].Value)
		assert.Equal(t, "input", metrics[0].Attributes["type"])
	})
}
