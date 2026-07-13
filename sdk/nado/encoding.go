package nado

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

type abiOrder struct {
	Sender         common.Address
	SubaccountName string
	PriceX18       *big.Int
	Amount         *big.Int
	Expiration     uint64
	Nonce          uint64
}

type abiSignedOrder struct {
	Order     abiOrder
	Signature []byte
}

func EncodeSignedOrder(order TxOrder, signature string) (string, error) {
	sender, err := decodeSender(order.Sender)
	if err != nil {
		return "", err
	}
	price, err := parseInteger("priceX18", order.PriceX18, true, 128)
	if err != nil {
		return "", err
	}
	amount, err := parseInteger("amount", order.Amount, true, 128)
	if err != nil {
		return "", err
	}
	expiration, err := parseInteger("expiration", order.Expiration, false, 64)
	if err != nil {
		return "", err
	}
	nonce, err := parseInteger("nonce", order.Nonce, false, 64)
	if err != nil {
		return "", err
	}
	if _, err := parseInteger("appendix", order.Appendix, false, 128); err != nil {
		return "", err
	}
	signatureBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(signatureBytes) != 65 {
		return "", fmt.Errorf("nado signed order: signature must be 65-byte hex")
	}

	subaccount, err := decodeSubaccountName(sender[20:])
	if err != nil {
		return "", err
	}
	tupleType, err := abi.NewType("tuple", "", []abi.ArgumentMarshaling{
		{Name: "order", Type: "tuple", Components: []abi.ArgumentMarshaling{
			{Name: "sender", Type: "address"},
			{Name: "subaccountName", Type: "string"},
			{Name: "priceX18", Type: "int128"},
			{Name: "amount", Type: "int128"},
			{Name: "expiration", Type: "uint64"},
			{Name: "nonce", Type: "uint64"},
		}},
		{Name: "signature", Type: "bytes"},
	})
	if err != nil {
		return "", fmt.Errorf("nado signed order ABI: %w", err)
	}
	encoded, err := (abi.Arguments{{Type: tupleType}}).Pack(abiSignedOrder{
		Order: abiOrder{
			Sender: common.BytesToAddress(sender[:20]), SubaccountName: subaccount,
			PriceX18: price, Amount: amount, Expiration: expiration.Uint64(), Nonce: nonce.Uint64(),
		},
		Signature: signatureBytes,
	})
	if err != nil {
		return "", fmt.Errorf("encode nado signed order: %w", err)
	}
	return "0x" + hex.EncodeToString(encoded), nil
}

func decodeSubaccountName(raw []byte) (string, error) {
	if len(raw) != 12 {
		return "", fmt.Errorf("nado signed order: subaccount must be 12 bytes")
	}
	if zero := bytes.IndexByte(raw, 0); zero >= 0 {
		if len(bytes.Trim(raw[zero:], "\x00")) != 0 {
			return "", fmt.Errorf("nado signed order: subaccount has non-zero bytes after padding")
		}
		raw = raw[:zero]
	}
	return string(raw), nil
}
