package starkcurve

import (
	"crypto/sha256"
	"errors"
	"io"
	"math/big"
)

const (
	compactStarkCurveBits = 8
	starkCurveBits        = 256
)

type CurvePoint struct {
	X *big.Int
	Y *big.Int
}

var (
	starkCurvePoints []CurvePoint
)

func InitStarkCurveParams() {
	starkCurvePoints = make([]CurvePoint, (starkCurveBits/compactBits+1)*(1<<compactBits))
	curve := NewStarkCurve()

	for i := 0; i < (starkCurveBits+compactBits-1)/compactBits; i++ {
		for j := 0; j < (1 << compactBits); j++ {
			v := j
			highestBitIdx, remainder := splitInt(v)

			idx := i * compactBits
			var points []*big.Int

			if idx+highestBitIdx-1 < 256 && idx+highestBitIdx-1 >= 0 {
				points = constPoints.ConstantPoints[idx+highestBitIdx-1]
			} else {
				points = []*big.Int{big.NewInt(0), big.NewInt(0)}
			}

			x := big.NewInt(0).Set(points[0])
			y := big.NewInt(0).Set(points[1])

			if remainder == 0 {
				starkCurvePoints[i<<compactBits+j] = CurvePoint{
					X: x,
					Y: y,
				}

			} else {
				x3 := starkCurvePoints[i<<compactBits+remainder].X
				y3 := starkCurvePoints[i<<compactBits+remainder].Y

				x5, y5 := curve.Add(x3, y3, x, y)

				starkCurvePoints[i<<compactBits+j] = CurvePoint{
					X: x5,
					Y: y5,
				}

			}
		}

	}
}

func (starkCurve *StarkCurve) ScalarBaseMultV3(k []byte) (*big.Int, *big.Int) {
	seenFirstTrue := false
	//firstPos := len(k)*8 - 1
	var x *big.Int
	var y *big.Int
	for i, byteData := range k {
		firstPos := (len(k)-1-i)*256 + int(byteData)
		starkCurvePoint := starkCurvePoints[firstPos]
		if byteData&0xFF != 0 {
			if !seenFirstTrue {
				seenFirstTrue = true
				x = big.NewInt(0).Set(starkCurvePoint.X)
				y = big.NewInt(0).Set(starkCurvePoint.Y)
			} else {
				tempx := big.NewInt(0).Set(starkCurvePoint.X)
				tempy := big.NewInt(0).Set(starkCurvePoint.Y)
				x, y = starkCurve.Add(tempx, tempy, x, y)
			}
		}

	}

	if !seenFirstTrue {
		return nil, nil
	}
	return x, y
}

// TODO: double check if it is okay
// GenerateKey returns a public/private key pair. The private key is generated
// using the given reader, which must return random data.
func (starkCurve *StarkCurve) GenerateKeyV3(rand io.Reader) (priv []byte, x, y *big.Int, err error) {
	byteLen := (starkCurve.BitSize + 7) >> 3
	priv = make([]byte, byteLen)

	for x == nil {
		_, err = io.ReadFull(rand, priv)
		if err != nil {
			return
		}
		// We have to mask off any excess bits in the case that the size of the
		// underlying field is not a whole number of bytes.
		priv[0] &= mask[starkCurve.BitSize%8]
		// This is because, in tests, rand will return all zeros and we don't
		// want to get the point at infinity and loop forever.
		priv[1] ^= 0x42
		x, y = starkCurve.ScalarBaseMultV3(priv)
	}
	return
}

func VerifyV3(hash []byte, pubkeyX, pubkeyY, r, s *big.Int) bool {
	hashInt := big.NewInt(0).SetBytes(hash)

	maxData := big.NewInt(1).Lsh(one, 251)
	if hashInt.Cmp(maxData) > 0 {
		return false
	}
	curve := NewStarkCurve()
	N := curve.N
	if r.Sign() <= 0 || s.Sign() <= 0 {
		return false
	}
	if r.Cmp(N) >= 0 || s.Cmp(N) >= 0 {
		return false
	}
	e := hashToInt(hash, curve.BitSize)
	w := new(big.Int).ModInverse(s, N)
	u1 := e.Mul(e, w)
	u1.Mod(u1, N)
	u2 := w.Mul(r, w)
	u2.Mod(u2, N)

	// Check if implements S1*g + S2*p
	var x, y *big.Int
	x1, y1 := curve.ScalarBaseMultV3(u1.Bytes())
	x2, y2 := curve.ScalarMult(pubkeyX, pubkeyY, u2.Bytes())
	x, y = curve.Add(x1, y1, x2, y2)

	if x.Sign() == 0 && y.Sign() == 0 {
		return false
	}
	x.Mod(x, N)
	return x.Cmp(r) == 0
}

// Reference https://github.com/apisit/rfc6979
func SignV3(privkey []byte, hash []byte) (*big.Int, *big.Int, error) {

	hashInt := big.NewInt(0).SetBytes(hash)
	one := big.NewInt(1)
	maxData := big.NewInt(1).Lsh(one, 251)
	if hashInt.Cmp(maxData) > 0 {
		return nil, nil, errors.New("hash cannot sign ")
	}
	curve := NewStarkCurve()
	privkeyInt := big.NewInt(0).SetBytes(privkey)
	N := curve.N
	r := big.NewInt(0)
	s := big.NewInt(0)
	// while True:
	// k = generate_k_rfc6979(msg_hash, priv_key, seed)
	// # Update seed for next iteration in case the value of k is bad.
	// if seed is None:
	// 	seed = 1
	// else:
	// 	seed += 1

	// # Cannot fail because 0 < k < EC_ORDER and EC_ORDER is prime.
	// x = ec_mult(k, EC_GEN, ALPHA, FIELD_PRIME)[0]

	// # DIFF: in classic ECDSA, we take int(x) % n.
	// r = int(x)
	// if not (1 <= r < 2**N_ELEMENT_BITS_ECDSA):
	// 	# Bad value. This fails with negligible probability.
	// 	continue

	// if (msg_hash + r * priv_key) % EC_ORDER == 0:
	// 	# Bad value. This fails with negligible probability.
	// 	continue

	// w = div_mod(k, msg_hash + r * priv_key, EC_ORDER)
	// if not (1 <= w < 2**N_ELEMENT_BITS_ECDSA):
	// 	# Bad value. This fails with negligible probability.
	// 	continue

	// s = inv_mod_curve_size(w)
	// return r, s
	generateSecret(N, sha256.New, hash, func(k *big.Int) bool {
		// fmt.Println("k ", k)
		inv := new(big.Int).ModInverse(k, N)
		r, _ = curve.ScalarBaseMultV3(k.Bytes())

		if r.Cmp(maxData) > 0 {
			return false
		}
		r.Mod(r, N)

		if r.Sign() == 0 {
			return false
		}

		e := hashToInt(hash, curve.BitSize)
		s = new(big.Int).Mul(privkeyInt, r)
		s.Add(s, e)
		tempData := big.NewInt(0)
		tempData = tempData.Mod(s, cfg.EcOrder)
		if tempData.Sign() == 0 {
			return false
		}
		// tempData = tempData.Set(s)
		// tempData.DivMod(k, tempData, cfg.EcOrder)
		s.Mul(s, inv)
		s.Mod(s, N)

		return s.Sign() != 0
	})
	return r, s, nil
}
