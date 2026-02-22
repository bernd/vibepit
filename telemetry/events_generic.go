package telemetry

import "github.com/bernd/vibepit/proxy"

type genericEventRenderer struct{}

func (genericEventRenderer) RenderLine(e proxy.TelemetryEvent) []EventSpan {
	return renderDefaultLine(e)
}

func (genericEventRenderer) RenderDetails(e proxy.TelemetryEvent) [][]EventSpan {
	return RenderAttrDetails(e, nil, nil)
}

func (genericEventRenderer) IsNoise(_ proxy.TelemetryEvent) bool {
	return false
}
