package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/bernd/vibepit/config"
	"github.com/bernd/vibepit/proxy"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ControlClient talks to a running proxy's embedded NATS control bus over mTLS.
type ControlClient struct {
	nc *nats.Conn
	js jetstream.JetStream
}

func NewControlClient(session *SessionInfo) (*ControlClient, error) {
	if session.ControlPort == "" {
		return nil, fmt.Errorf("missing control API port for session %q", session.SessionID)
	}
	tlsCfg, err := LoadSessionTLSConfig(session.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load TLS credentials: %w", err)
	}
	nc, err := nats.Connect(
		fmt.Sprintf("tls://127.0.0.1:%s", session.ControlPort),
		nats.Secure(tlsCfg),
		nats.Timeout(5*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return nil, fmt.Errorf("connect control bus: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	return &ControlClient{nc: nc, js: js}, nil
}

func (c *ControlClient) Close() { c.nc.Close() }

func decodeReply(msg *nats.Msg, into any) error {
	if code := msg.Header.Get("Nats-Service-Error-Code"); code != "" {
		return fmt.Errorf("%s: %s", code, msg.Header.Get("Nats-Service-Error"))
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(msg.Data, into)
}

func (c *ControlClient) request(subj string, body any, into any) error {
	data := []byte("{}")
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		data = b
	}
	msg, err := c.nc.Request(subj, data, 5*time.Second)
	if err != nil {
		return fmt.Errorf("request %s: %w", subj, err)
	}
	return decodeReply(msg, into)
}

func (c *ControlClient) Stats() (map[string]proxy.DomainStats, error) {
	var stats map[string]proxy.DomainStats
	return stats, c.request(proxy.SubjectStats, nil, &stats)
}

func (c *ControlClient) Config() (*config.MergedConfig, error) {
	var cfg config.MergedConfig
	return &cfg, c.request(proxy.SubjectConfig, nil, &cfg)
}

func (c *ControlClient) AllowHTTP(entries []string) ([]string, error) {
	return c.postAllow(proxy.SubjectAllowHTTP, entries)
}

func (c *ControlClient) AllowDNS(entries []string) ([]string, error) {
	return c.postAllow(proxy.SubjectAllowDNS, entries)
}

func (c *ControlClient) postAllow(subj string, entries []string) ([]string, error) {
	var result struct {
		Added []string `json:"added"`
	}
	if err := c.request(subj, map[string]any{"entries": entries}, &result); err != nil {
		return nil, err
	}
	return result.Added, nil
}

// SubscribeLogs delivers retained history then live entries in stream order.
// The returned function stops the consumer.
func (c *ControlClient) SubscribeLogs(ch chan<- proxy.LogEntry) (func(), error) {
	ctx := context.Background()
	stream, err := c.js.Stream(ctx, proxy.StreamLogs)
	if err != nil {
		return nil, fmt.Errorf("stream: %w", err)
	}
	cons, err := stream.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("ordered consumer: %w", err)
	}
	cc, err := cons.Consume(func(m jetstream.Msg) {
		var e proxy.LogEntry
		if json.Unmarshal(m.Data(), &e) == nil {
			ch <- e
		}
	})
	if err != nil {
		return nil, fmt.Errorf("consume: %w", err)
	}
	return cc.Stop, nil
}
