package perp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/exchanges/sdk/edgex/starkcurve"

	"golang.org/x/crypto/sha3"
)

// GenerateSignature generates the signature for EdgeX API
func GenerateSignature(privateKeyHex string, timestamp int64, method, path string, body string, params map[string]interface{}) (string, error) {
	// 1. Prepare the content to sign
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d", timestamp))
	sb.WriteString(strings.ToUpper(method))
	sb.WriteString(path)

	if body != "" {
		if len(params) > 0 {
			sb.WriteString(GetValue(params))
		} else {
			// Try to unmarshal body
			var bodyMap map[string]interface{}
			if err := json.Unmarshal([]byte(body), &bodyMap); err == nil {
				sb.WriteString(GetValue(bodyMap))
			} else {
				// If not map, maybe array?
				var bodyArr []interface{}
				if err := json.Unmarshal([]byte(body), &bodyArr); err == nil {
					sb.WriteString(GetValue(bodyArr))
				} else {
					sb.WriteString(body)
				}
			}
		}
	} else if len(params) > 0 {
		// GET request params
		// Sort keys
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		var paramPairs []string
		for _, k := range keys {
			paramPairs = append(paramPairs, fmt.Sprintf("%s=%v", k, params[k]))
		}
		sb.WriteString(strings.Join(paramPairs, "&"))
	}

	signContent := sb.String()

	// 2. Hash using SHA3-256 (Keccak-256)
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write([]byte(signContent))
	hash := hasher.Sum(nil)

	// 3. Sign using StarkCurve
	// Decode private key
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	privBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}

	// Modulo hash with curve order N
	msgHashInt := big.NewInt(0).SetBytes(hash)
	curve := starkcurve.NewStarkCurve()
	msgHashInt = msgHashInt.Mod(msgHashInt, curve.N)

	r, s, err := starkcurve.Sign(privBytes, msgHashInt.Bytes())
	if err != nil {
		return "", fmt.Errorf("failed to sign: %w", err)
	}

	// Calculate Public Key Y for signature format (r + s + y)
	_, pubY := curve.ScalarBaseMult(privBytes)
	if pubY == nil {
		return "", fmt.Errorf("failed to calculate public key: result is nil (invalid private key?)")
	}
	yBytes := pubY.Bytes()

	// Pad r, s, y to 32 bytes
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	rPad := make([]byte, 32-len(rBytes))
	sPad := make([]byte, 32-len(sBytes))
	yPad := make([]byte, 32-len(yBytes))

	// Format: r + s + y
	return fmt.Sprintf("%x%x%x%x%x%x", rPad, rBytes, sPad, sBytes, yPad, yBytes), nil
}

// GetValue converts a value to a string representation for signing (Canonicalization)
func GetValue(data interface{}) string {
	switch v := data.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strings.ToLower(fmt.Sprintf("%v", v))
	case int, int32, int64, float32, float64:
		return fmt.Sprintf("%v", v)
	case []interface{}:
		if len(v) == 0 {
			return ""
		}
		var values []string
		for _, item := range v {
			values = append(values, GetValue(item))
		}
		return strings.Join(values, "&")
	case []string:
		if len(v) == 0 {
			return ""
		}
		return strings.Join(v, "&")
	case map[string]interface{}:
		// Convert all values to strings and sort by keys
		sortedMap := make(map[string]string)
		for key, val := range v {
			sortedMap[key] = GetValue(val)
		}

		// Get sorted keys
		keys := make([]string, 0, len(sortedMap))
		for k := range sortedMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		// Build key=value pairs
		var pairs []string
		for _, k := range keys {
			pairs = append(pairs, fmt.Sprintf("%s=%s", k, sortedMap[k]))
		}
		return strings.Join(pairs, "&")
	default:
		return fmt.Sprintf("%v", v)
	}
}

// GenerateSignatureForWS generates the signature for WebSocket authentication
func GenerateSignatureForWS(privateKeyHex string, timestamp int64) (string, error) {
	// For WS, we sign: timestamp + "GET" + "/api/v1/private/ws"
	path := "/api/v1/private/ws"
	return GenerateSignature(privateKeyHex, timestamp, "GET", path, "", nil)
}

// SignL2 signs a message hash using the Stark private key (Pedersen hash)
func SignL2(privateKeyHex string, msgHash []byte) (*L2Signature, error) {
	if len(privateKeyHex) > 2 && privateKeyHex[:2] == "0x" {
		privateKeyHex = privateKeyHex[2:]
	}
	privBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key hex: %w", err)
	}

	// Modulo hash with curve order N
	msgHashInt := big.NewInt(0).SetBytes(msgHash)
	curve := starkcurve.NewStarkCurve()
	msgHashInt = msgHashInt.Mod(msgHashInt, curve.N)

	r, s, err := starkcurve.Sign(privBytes, msgHashInt.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign: %w", err)
	}

	// Pad r and s to 32 bytes
	rBytes := r.Bytes()
	sBytes := s.Bytes()

	rPad := make([]byte, 32-len(rBytes))
	sPad := make([]byte, 32-len(sBytes))

	return &L2Signature{
		R: fmt.Sprintf("%x%x", rPad, rBytes),
		S: fmt.Sprintf("%x%x", sPad, sBytes),
		V: "",
	}, nil
}

const (
	LimitOrderWithFeeType = 3
)

// CalcNonce calculates a nonce from source string
func CalcNonce(src string) int64 {
	h := sha256.New()
	h.Write([]byte(src))
	hash := fmt.Sprintf("%x", h.Sum(nil))

	result, _ := big.NewInt(0).SetString(string(hash[:8]), 16)
	return result.Int64()
}

// CalcLimitOrderHash calculates the hash for a limit order
func CalcLimitOrderHash(assetIdSynthetic, assetIdCollateral, assetIdFee string, isBuyingSynthetic bool, amountSynthetic, amountCollateral, amountFee, nonce, positionID, expirationTimestamp int64) []byte {
	// Remove assetIdSynthetic, assetIdCollateral, assetIdFee 0x prefix if exists
	if len(assetIdSynthetic) > 2 && assetIdSynthetic[:2] == "0x" {
		assetIdSynthetic = assetIdSynthetic[2:]
	}
	if len(assetIdCollateral) > 2 && assetIdCollateral[:2] == "0x" {
		assetIdCollateral = assetIdCollateral[2:]
	}
	if len(assetIdFee) > 2 && assetIdFee[:2] == "0x" {
		assetIdFee = assetIdFee[2:]
	}

	var asset_id_sell *big.Int
	var asset_id_buy *big.Int
	var amount_sell, amount_buy *big.Int
	if isBuyingSynthetic {
		asset_id_sell, _ = big.NewInt(0).SetString(assetIdCollateral, 16)
		asset_id_buy, _ = big.NewInt(0).SetString(assetIdSynthetic, 16)
		amount_sell = big.NewInt(amountCollateral)
		amount_buy = big.NewInt(amountSynthetic)
	} else {
		asset_id_sell, _ = big.NewInt(0).SetString(assetIdSynthetic, 16)
		asset_id_buy, _ = big.NewInt(0).SetString(assetIdCollateral, 16)
		amount_sell = big.NewInt(amountSynthetic)
		amount_buy = big.NewInt(amountCollateral)
	}
	asset_id_fee, _ := big.NewInt(0).SetString(assetIdFee, 16)
	msg := starkcurve.CalcHash([]*big.Int{asset_id_sell, asset_id_buy})
	msgInt := big.NewInt(0).SetBytes(msg)
	msg = starkcurve.CalcHash([]*big.Int{msgInt, asset_id_fee})

	// packed_message0 = amount_sell
	// packed_message0 = packed_message0 * 2**64 + amount_buy
	// packed_message0 = packed_message0 * 2**64 + max_amount_fee
	// packed_message0 = packed_message0 * 2**32 + nonce
	packed_message0 := big.NewInt(0).Set(amount_sell)
	packed_message0 = packed_message0.Lsh(packed_message0, 64)
	packed_message0 = packed_message0.Add(packed_message0, amount_buy)
	max_amount_fee := big.NewInt(amountFee)
	packed_message0 = packed_message0.Lsh(packed_message0, 64)
	packed_message0 = packed_message0.Add(packed_message0, max_amount_fee)
	nonceInt := big.NewInt(nonce)
	packed_message0 = packed_message0.Lsh(packed_message0, 32)
	packed_message0 = packed_message0.Add(packed_message0, nonceInt)
	msgInt = big.NewInt(0).SetBytes(msg)
	msg = starkcurve.CalcHash([]*big.Int{msgInt, packed_message0})

	// packed_message1 = LIMIT_ORDER_WITH_FEES
	// packed_message1 = packed_message1 * 2**64 + position_id
	// packed_message1 = packed_message1 * 2**64 + position_id
	// packed_message1 = packed_message1 * 2**64 + position_id
	// packed_message1 = packed_message1 * 2**32 + expiration_timestamp
	// packed_message1 = packed_message1 * 2**17  # Padding.
	packed_message1 := big.NewInt(LimitOrderWithFeeType)
	packed_message1 = packed_message1.Lsh(packed_message1, 64)
	positionIDInt := big.NewInt(positionID)
	packed_message1 = packed_message1.Add(packed_message1, positionIDInt)
	packed_message1 = packed_message1.Lsh(packed_message1, 64)
	packed_message1 = packed_message1.Add(packed_message1, positionIDInt)
	packed_message1 = packed_message1.Lsh(packed_message1, 64)
	packed_message1 = packed_message1.Add(packed_message1, positionIDInt)
	expirationTimestampInt := big.NewInt(expirationTimestamp)
	packed_message1 = packed_message1.Lsh(packed_message1, 32)
	packed_message1 = packed_message1.Add(packed_message1, expirationTimestampInt)
	packed_message1 = packed_message1.Lsh(packed_message1, 17)
	msgInt = big.NewInt(0).SetBytes(msg)
	msg = starkcurve.CalcHash([]*big.Int{msgInt, packed_message1})

	return msg
}

func GetRandomClientId() string {
	nanoTimestamp := time.Now().UnixNano()
	return strconv.FormatInt(nanoTimestamp, 10)
}

func ToBigInt(number string) *big.Int {
	if number == "" {
		return big.NewInt(0)
	}
	if strings.HasPrefix(number, "0x") {
		val, _ := new(big.Int).SetString(number[2:], 16)
		return val
	}
	val, _ := new(big.Int).SetString(number, 10)
	return val
}

func HexToBigInteger(hex string) (*big.Int, error) {
	if len(hex) > 2 && hex[:2] == "0x" {
		hex = hex[2:]
	}
	result := new(big.Int)
	result, ok := result.SetString(hex, 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex string: %s", hex)
	}
	return result, nil
}
