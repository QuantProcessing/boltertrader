package wsdispatch

import "math/rand"

func GenerateRandomID() int64 {
	return rand.Int63()
}
