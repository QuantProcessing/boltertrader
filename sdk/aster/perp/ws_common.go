package perp

type MsgDispatcher interface {
	Dispatch(data []byte) error
}
