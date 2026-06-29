package grvt

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

const (
	PriceMultiplier = 1_000_000_000
)

// EIP-712 Domain
func GetEIP712Domain(chainID string) apitypes.TypedDataDomain {
	result, err := strconv.ParseInt(chainID, 10, 64)
	if err != nil {
		panic(err)
	}
	return apitypes.TypedDataDomain{
		Name:    "GRVT Exchange",
		Version: "0",
		ChainId: math.NewHexOrDecimal256(result),
	}
}

// Order Types Definition
var OrderTypes = apitypes.Types{
	"EIP712Domain": {
		{Name: "name", Type: "string"},
		{Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"},
	},
	"Order": {
		{Name: "subAccountID", Type: "uint64"},
		{Name: "isMarket", Type: "bool"},
		{Name: "timeInForce", Type: "uint8"},
		{Name: "postOnly", Type: "bool"},
		{Name: "reduceOnly", Type: "bool"},
		{Name: "legs", Type: "OrderLeg[]"},
		{Name: "nonce", Type: "uint32"},
		{Name: "expiration", Type: "int64"},
	},
	"OrderLeg": {
		{Name: "assetID", Type: "uint256"},
		{Name: "contractSize", Type: "uint64"},
		{Name: "limitPrice", Type: "uint64"},
		{Name: "isBuyingContract", Type: "bool"},
	},
}

func SignOrder(order *OrderRequest, privateKeyHex string, chainID string, instruments map[string]Instrument) error {
	// 1. Prepare Message
	legs := make([]map[string]interface{}, len(order.Legs))
	for i, leg := range order.Legs {
		instrument, ok := instruments[leg.Instrument]
		if !ok {
			return fmt.Errorf("instrument not found: %s", leg.Instrument)
		}

		// Contract Size: float(leg.size) * 10^base_decimals
		sizeFloat, err := strconv.ParseFloat(leg.Size, 64)
		if err != nil {
			return fmt.Errorf("invalid size format: %s", leg.Size)
		}
		// 10^base_decimals
		multiplier := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(instrument.BaseDecimals)), nil))
		contractSize := new(big.Float).Mul(big.NewFloat(sizeFloat), multiplier)
		contractSizeInt, _ := contractSize.Int64()

		// Limit Price: float(leg.limit_price) * PRICE_MULTIPLIER
		priceFloat, err := strconv.ParseFloat(leg.LimitPrice, 64)
		if err != nil {
			return fmt.Errorf("invalid price format: %s", leg.LimitPrice)
		}
		// PRICE_MULTIPLIER
		limitPrice := new(big.Float).Mul(big.NewFloat(priceFloat), big.NewFloat(float64(PriceMultiplier)))
		limitPriceInt, _ := limitPrice.Int64()
		if order.IsMarket {
			limitPriceInt = 0
		}

		// Asset ID from Instrument Hash (hex string -> uint256)
		assetIDBig, ok := new(big.Int).SetString(instrument.InstrumentHash, 0) // 0 detects base (likely hex with 0x)
		if !ok {
			return fmt.Errorf("invalid instrument hash: %s", instrument.InstrumentHash)
		}

		legs[i] = map[string]interface{}{
			"assetID": math.NewHexOrDecimal256(assetIDBig.Int64()), // Wrong: Expects *hexutil.Big or compatible interface for uint256?
			// apitypes handles *math.HexOrDecimal256 which wraps *big.Int
			// math.NewHexOrDecimal256 takes int64. We need validation if assetID fits in int64?
			// AssetID is uint256 (hash), definitely DOES NOT fit in int64.
			// We must use (*math.HexOrDecimal256)(bigInt) if possible or construct it manually.
			// Actually math.HexOrDecimal256 is just `type HexOrDecimal256 big.Int`.
			// So we can convert.
		}

		// Correct way to populate uint256 fields for apitypes:
		legs[i]["assetID"] = (*math.HexOrDecimal256)(assetIDBig)
		legs[i]["contractSize"] = math.NewHexOrDecimal256(contractSizeInt)
		legs[i]["limitPrice"] = math.NewHexOrDecimal256(limitPriceInt)
		legs[i]["isBuyingContract"] = leg.IsBuyintAsset
	}

	// Parse Expiration string to int64
	expInt, err := strconv.ParseInt(order.Signature.Expiration, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid expiration format: %s", order.Signature.Expiration)
	}

	message := map[string]interface{}{
		"subAccountID": math.NewHexOrDecimal256(int64(order.SubAccountID)),
		"isMarket":     order.IsMarket,
		"timeInForce":  math.NewHexOrDecimal256(int64(SignTimeInForceMap[order.TimeInForce])),
		"postOnly":     order.PostOnly,
		"reduceOnly":   order.ReduceOnly,
		"legs":         legs,
		"nonce":        math.NewHexOrDecimal256(int64(order.Signature.Nonce)),
		"expiration":   math.NewHexOrDecimal256(expInt),
	}

	typedData := apitypes.TypedData{
		Types:       OrderTypes,
		PrimaryType: "Order",
		Domain:      GetEIP712Domain(chainID),
		Message:     message,
	}

	// 2. Hash
	domainSeparator, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return fmt.Errorf("failed to hash domain: %w", err)
	}
	typedDataHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return fmt.Errorf("failed to hash message: %w", err)
	}
	rawData := []byte(fmt.Sprintf("\x19\x01%s%s", string(domainSeparator), string(typedDataHash)))
	hash := crypto.Keccak256(rawData)

	// 3. Sign
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	pk, err := crypto.HexToECDSA(privateKeyHex)
	if err != nil {
		return fmt.Errorf("invalid private key: %w", err)
	}

	signature, err := crypto.Sign(hash, pk)
	if err != nil {
		return fmt.Errorf("failed to sign: %w", err)
	}

	// 4. Set Signature
	// R: 32 bytes, S: 32 bytes, V: 1 byte
	rBytes := signature[:32]
	sBytes := signature[32:64]
	v := int(signature[64])

	// Format R and S as 0x-prefixed hex strings
	order.Signature.R = fmt.Sprintf("0x%x", rBytes)
	order.Signature.S = fmt.Sprintf("0x%x", sBytes)
	// V needs to be shifted?
	// Ethereum Sign produces V as 0 or 1 (usually).
	// EIP-712 / generic Ethereum signature recovery usually expects 27 or 28?
	// The Python SDK calls `signed_message.v`. Eth account usually returns 27/28.
	// crypto.Sign returns V as 0/1. We need to add 27.
	order.Signature.V = v + 27

	// Set Signer
	publicKey := pk.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return fmt.Errorf("error casting public key to ECDSA")
	}
	order.Signature.Signer = crypto.PubkeyToAddress(*publicKeyECDSA).Hex()
	order.Signature.ChainID = chainID

	return nil
}
