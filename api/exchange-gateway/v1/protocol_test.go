package gatewayv1

import "testing"

func TestControlEnforcesTopicLimitAndPrivateCursorShape(t *testing.T) {
	topics := make([]TopicRequest, MaxActiveTopics)
	for index := range topics {
		topics[index] = TopicRequest{Topic: TopicMarkPrice, InstrumentID: "instrument"}
	}
	if err := (Control{Operation: OperationSubscribe, RequestID: "request", Topics: topics}).Validate(); err != nil {
		t.Fatalf("maximum topic control rejected: %v", err)
	}
	topics = append(topics, TopicRequest{Topic: TopicVenueConnectivity})
	if err := (Control{Operation: OperationSubscribe, RequestID: "request", Topics: topics}).Validate(); err == nil {
		t.Fatal("control above the topic limit was accepted")
	}
	if err := (TopicRequest{Topic: TopicAccountEvents, Cursor: "opaque"}).Validate(); err != nil {
		t.Fatalf("account cursor request rejected: %v", err)
	}
}

func TestUnsubscribeRequiresOpaqueServerSubscriptionID(t *testing.T) {
	control := Control{Operation: OperationUnsubscribe, RequestID: "request", Topics: []TopicRequest{{Topic: TopicMarkPrice}}}
	if err := control.Validate(); err == nil {
		t.Fatal("unsubscribe without subscriptionId was accepted")
	}
	control.Topics[0].SubscriptionID = "market-1"
	if err := control.Validate(); err != nil {
		t.Fatalf("valid unsubscribe rejected: %v", err)
	}
}

func TestRetiredApplicationTopicsAndReasonsAreRejected(t *testing.T) {
	for _, request := range []TopicRequest{
		{Topic: "arbitrage.opportunities"},
		{Topic: TopicMarkPrice, InstrumentID: "instrument", InternalPurpose: "arbitrage_top"},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("retired subscription accepted: %+v", request)
		}
	}
}
