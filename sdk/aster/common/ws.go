package common

type MsgDispatcher interface {
	Dispatch(data []byte) error
}
