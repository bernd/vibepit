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
	return []byte(fmt.Sprintf(mavenSettingsTemplate, host, port))
}
