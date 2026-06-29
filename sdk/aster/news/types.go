package news

// WsNewsEvent represents a news event from the WebSocket
type WsNewsEvent struct {
	CatalogId   int64  `json:"catalogId"`
	CatalogName string `json:"catalogName"`
	PublishDate int64  `json:"publishDate"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Disclaimer  string `json:"disclaimer"`
}

// WsResponse represents a generic response from the WebSocket
type WsResponse struct {
	Type  string `json:"type"`
	Topic string `json:"topic"`
	Data  string `json:"data"` // Data is a JSON string containing WsNewsEvent
}

// WsRequest represents a request to the WebSocket
type WsRequest struct {
	Command string `json:"command"`
	Value   string `json:"value"`
}
