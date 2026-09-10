package monitor

import (
	"context"
	"errors"
	"io"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/clients/gateway"
)

// Run uses only the public market socket. Gateway owns reconnection and venue
// collection; monitor owns source freshness, the rule, and its local checkpoint.
// The state directory must exist. Output is newline-delimited JSON on stdout.
func Run(ctx context.Context, client *gateway.Client, config Config, statePath string, output io.Writer) error {
	if client == nil || output == nil {
		return errors.New("monitor requires a Gateway client and event output")
	}
	f, e, err := openState(statePath, config, time.Now())
	if err != nil {
		return err
	}
	defer f.close()
	// Replay any saved output BEFORE allowing fresh observations to change it.
	if err := f.flush(e, output); err != nil {
		return err
	}
	if err := e.advance(time.Now()); err != nil {
		return err
	}
	if err := f.flush(e, output); err != nil {
		return err
	}
	topics := make([]gateway.MarketTopic, 0, len(config.Instruments))
	for _, id := range e.config.Instruments {
		topics = append(topics, gateway.MarketTopic{Topic: gateway.BestBidOffer, InstrumentID: id})
	}
	stream, err := client.SubscribeMarkets(ctx, topics, gateway.StreamOptions{})
	if err != nil {
		return err
	}
	defer stream.Close()
	generations := make(map[string]uint64, len(topics))
	timer := time.NewTicker(min(time.Second, config.MaxGap/4))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return f.flush(e, output)
		case <-timer.C:
			if err := e.advance(time.Now()); err != nil {
				return err
			}
		case event, ok := <-stream.Events():
			if err := e.advance(time.Now()); err != nil {
				return err
			}
			if !ok {
				if stream.Err() != nil {
					for _, id := range e.config.Instruments {
						e.interrupt(id, "stream_stopped")
					}
				}
				if err := f.flush(e, output); err != nil {
					return err
				}
				return stream.Err()
			}
			id := event.Topic.InstrumentID
			if e.Instruments[id] == nil || event.Topic.Topic != gateway.BestBidOffer {
				return errors.New("unexpected monitor subscription scope")
			}
			if event.Message.Type == wire.MessageResyncRequired {
				e.interrupt(id, "stream_resync")
			} else {
				if previous := generations[id]; previous != 0 && previous != event.Message.Generation {
					e.interrupt(id, "stream_generation_changed")
				}
				generations[id] = event.Message.Generation
				bbo, err := gateway.DecodeBBO(event.Message)
				if err != nil {
					return err
				}
				// A zero quantity is not executable top-of-book liquidity.
				_, bidOK := price(bbo.Bid.Quantity)
				_, askOK := price(bbo.Ask.Quantity)
				if bidOK && askOK {
					e.observe(id, event.Message.ObservedAt, bbo.Bid.Price, bbo.Ask.Price)
				}
			}
		}
		if len(e.Pending) > 0 || e.now.Sub(e.SavedAt) >= time.Second {
			if err := f.flush(e, output); err != nil {
				return err
			}
		}
	}
}
