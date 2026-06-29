package perp

const (
	WSBaseURL = "wss://fstream.asterdex.com/ws"
)

type MsgDispatcher interface {
	Dispatch(data []byte) error
}
