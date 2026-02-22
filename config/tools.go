package config

import "fmt"

const mavenSettingsTemplate = `<settings>
  <proxies>
    <proxy>
      <id>vibepit-proxy</id>
      <active>true</active>
      <protocol>https</protocol>
      <host>%s</host>
      <port>%d</port>
    </proxy>
  </proxies>
</settings>
`

func MavenSettings(host string, port int) []byte {
	return fmt.Appendf(nil, mavenSettingsTemplate, host, port)
}

const codexConfigTemplate = `[otel]
exporter = { otlp-http = { endpoint = "http://%s:%d/v1/logs", protocol = "binary" } }
metrics_exporter = { otlp-http = { endpoint = "http://%s:%d/v1/metrics", protocol = "binary" } }
log_user_prompt = true
`

func CodexConfig(host string, port int) []byte {
	return fmt.Appendf(nil, codexConfigTemplate, host, port, host, port)
}
