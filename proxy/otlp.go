package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"golang.org/x/time/rate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

const maxOTLPBodySize = 4 * 1024 * 1024 // 4 MB

// OTLPReceiver handles incoming OTLP HTTP/protobuf requests and writes
// decoded telemetry into a TelemetryBuffer.
type OTLPReceiver struct {
	mux     *http.ServeMux
	buf     *TelemetryBuffer
	limiter *rate.Limiter
}

func NewOTLPReceiver(buf *TelemetryBuffer) *OTLPReceiver {
	r := &OTLPReceiver{
		mux:     http.NewServeMux(),
		buf:     buf,
		limiter: rate.NewLimiter(100, 20), // 100 req/s, burst 20
	}
	r.mux.HandleFunc("POST /v1/logs", r.handleLogs)
	r.mux.HandleFunc("POST /v1/metrics", r.handleMetrics)
	return r
}

func (r *OTLPReceiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.limiter.Allow() {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	r.mux.ServeHTTP(w, req)
}

func (r *OTLPReceiver) handleLogs(w http.ResponseWriter, req *http.Request) {
	body, err := readLimited(req, maxOTLPBodySize)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var exportReq collectorlogs.ExportLogsServiceRequest
	if err := proto.Unmarshal(body, &exportReq); err != nil {
		http.Error(w, "invalid protobuf", http.StatusBadRequest)
		return
	}

	for _, rl := range exportReq.ResourceLogs {
		agent := extractServiceName(rl.Resource)
		for _, sl := range rl.ScopeLogs {
			for _, lr := range sl.LogRecords {
				event := TelemetryEvent{
					Time:  time.Unix(0, int64(lr.TimeUnixNano)),
					Agent: agent,
					Attrs: flattenAttributes(lr.Attributes),
				}
				if name, ok := event.Attrs["event.name"]; ok {
					event.EventName = name
					delete(event.Attrs, "event.name")
				}
				if raw, err := protojson.Marshal(lr); err == nil {
					event.RawLog = raw
				}
				r.buf.AddEvent(event)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "{}")
}

func (r *OTLPReceiver) handleMetrics(w http.ResponseWriter, req *http.Request) {
	body, err := readLimited(req, maxOTLPBodySize)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var exportReq collectormetrics.ExportMetricsServiceRequest
	if err := proto.Unmarshal(body, &exportReq); err != nil {
		http.Error(w, "invalid protobuf", http.StatusBadRequest)
		return
	}

	for _, rm := range exportReq.ResourceMetrics {
		agent := extractServiceName(rm.Resource)
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				var rawMetric json.RawMessage
				if raw, err := protojson.Marshal(m); err == nil {
					rawMetric = raw
				}
				for _, dp := range extractDataPoints(m) {
					r.buf.UpdateMetric(MetricSummary{
						Name:       m.Name,
						Agent:      agent,
						Value:      dp.value,
						IsDelta:    dp.isDelta,
						Attributes: dp.attrs,
						RawMetric:  rawMetric,
					})
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "{}")
}

type dataPoint struct {
	value   float64
	attrs   map[string]string
	isDelta bool
}

func extractDataPoints(m *metricspb.Metric) []dataPoint {
	switch d := m.Data.(type) {
	case *metricspb.Metric_Sum:
		isDelta := d.Sum.AggregationTemporality == metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA
		return numberDataPoints(d.Sum.DataPoints, isDelta)
	case *metricspb.Metric_Gauge:
		return numberDataPoints(d.Gauge.DataPoints, false)
	}
	return nil
}

func numberDataPoints(dps []*metricspb.NumberDataPoint, isDelta bool) []dataPoint {
	result := make([]dataPoint, 0, len(dps))
	for _, dp := range dps {
		var val float64
		switch v := dp.Value.(type) {
		case *metricspb.NumberDataPoint_AsDouble:
			val = v.AsDouble
		case *metricspb.NumberDataPoint_AsInt:
			val = float64(v.AsInt)
		}
		result = append(result, dataPoint{
			value:   val,
			attrs:   flattenAttributes(dp.Attributes),
			isDelta: isDelta,
		})
	}
	return result
}

func readLimited(req *http.Request, maxBytes int64) ([]byte, error) {
	limited := http.MaxBytesReader(nil, req.Body, maxBytes)
	defer limited.Close()
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func extractServiceName(res *resourcepb.Resource) string {
	if res == nil {
		return "unknown"
	}
	for _, attr := range res.Attributes {
		if attr.Key == "service.name" {
			if sv, ok := attr.Value.Value.(*commonpb.AnyValue_StringValue); ok {
				name := truncate(sv.StringValue, 64)
				return name
			}
		}
	}
	return "unknown"
}

func flattenAttributes(attrs []*commonpb.KeyValue) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	result := make(map[string]string, len(attrs))
	for _, kv := range attrs {
		result[kv.Key] = anyValueToString(kv.Value)
	}
	return result
}

func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_IntValue:
		return fmt.Sprintf("%d", val.IntValue)
	case *commonpb.AnyValue_DoubleValue:
		return fmt.Sprintf("%g", val.DoubleValue)
	case *commonpb.AnyValue_BoolValue:
		return fmt.Sprintf("%t", val.BoolValue)
	default:
		return fmt.Sprintf("%v", v.Value)
	}
}
