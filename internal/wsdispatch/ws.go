package wsdispatch

type MsgDispatcher interface {
	Dispatch(data []byte) error
}
