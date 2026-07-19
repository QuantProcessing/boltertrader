package factoryclient

import (
	"errors"
	"fmt"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type clientMeta struct {
	venue   exchange.Venue
	product exchange.Product
}

func (meta clientMeta) redactedString() string {
	return fmt.Sprintf("exchange/factory.Client{venue:%q, product:%q, credentials:redacted}", meta.venue, meta.product)
}

func withExchangeOperation(err error, operation string) error {
	if err == nil {
		return nil
	}
	var normalized *exchange.Error
	if !errors.As(err, &normalized) {
		return err
	}
	details := normalized.Details()
	details.Operation = operation
	return exchange.NewError(normalized.Kind(), details)
}

func boundedOrderPage(orders []exchange.Order, limit int, cursor string) exchange.OrderPage {
	if limit > 0 && len(orders) > limit {
		orders = orders[:limit]
	}
	return exchange.OrderPage{
		Orders: orders,
		Page: exchange.PageInfo{
			Cursor: cursor,
			Limit:  limit,
		},
	}
}

type spotClient struct {
	meta clientMeta
}

func (client *spotClient) String() string {
	if client == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.meta.redactedString()
}

func (client *spotClient) GoString() string {
	return client.String()
}

type perpClient struct {
	meta clientMeta
}

func (client *perpClient) String() string {
	if client == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.meta.redactedString()
}

func (client *perpClient) GoString() string {
	return client.String()
}
