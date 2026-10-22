// Package bus carries domain events between modules in-process and, when brokers
// are configured, mirrors them to a Kafka-compatible log (Redpanda) via franz-go.
package bus

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Event is a domain event.
type Event struct {
	Topic    string          `json:"topic"`
	TenantID uuid.UUID       `json:"tenantId"`
	Key      string          `json:"key,omitempty"`
	Data     json.RawMessage `json:"data"`
}

// Subscriber handles events synchronously after the publishing transaction commits.
type Subscriber func(ctx context.Context, e Event)

// Bus is the module event bus.
type Bus struct {
	mu    sync.RWMutex
	subs  map[string][]Subscriber
	kafka *kgo.Client
}

// New creates a bus; brokers may be empty.
func New(brokers []string) (*Bus, error) {
	b := &Bus{subs: map[string][]Subscriber{}}
	if len(brokers) > 0 {
		cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.AllowAutoTopicCreation(), kgo.RequiredAcks(kgo.AllISRAcks()))
		if err != nil {
			return nil, err
		}
		b.kafka = cl
	}
	return b, nil
}

// Close flushes and closes the Kafka client.
func (b *Bus) Close() {
	if b.kafka != nil {
		b.kafka.Close()
	}
}

// Subscribe registers fn for a topic ("*" receives all).
func (b *Bus) Subscribe(topic string, fn Subscriber) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[topic] = append(b.subs[topic], fn)
}

// Publish dispatches e to subscribers and mirrors it to Kafka when configured.
func (b *Bus) Publish(ctx context.Context, topic string, tenant uuid.UUID, key string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		slog.Error("bus marshal", "topic", topic, "err", err)
		return
	}
	e := Event{Topic: topic, TenantID: tenant, Key: key, Data: raw}
	b.mu.RLock()
	subs := append(append([]Subscriber{}, b.subs[topic]...), b.subs["*"]...)
	b.mu.RUnlock()
	for _, s := range subs {
		func() {
			defer func() {
				if p := recover(); p != nil {
					slog.Error("bus subscriber panic", "topic", topic, "panic", p)
				}
			}()
			s(ctx, e)
		}()
	}
	if b.kafka != nil {
		v, _ := json.Marshal(e)
		b.kafka.Produce(ctx, &kgo.Record{Topic: "openspoor." + topic, Key: []byte(tenant.String() + ":" + key), Value: v}, func(_ *kgo.Record, err error) {
			if err != nil {
				slog.Warn("kafka produce failed", "topic", topic, "err", err)
			}
		})
	}
}

// ProduceSync writes records to Kafka and waits for broker acknowledgement.
// It returns false when no broker is configured.
func (b *Bus) ProduceSync(ctx context.Context, topic string, recs [][]byte) (bool, error) {
	if b.kafka == nil {
		return false, nil
	}
	rs := make([]*kgo.Record, len(recs))
	for i, v := range recs {
		rs[i] = &kgo.Record{Topic: "openspoor." + topic, Value: v}
	}
	return true, b.kafka.ProduceSync(ctx, rs...).FirstErr()
}
