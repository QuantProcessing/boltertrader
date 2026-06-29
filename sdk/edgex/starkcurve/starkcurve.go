package starkcurve

import (
	"crypto/elliptic"
	"fmt"
	"io"
	"math/big"
)

// Copyright 2010 The Go Authors. All rights reserved.
// Copyright 2022 Bebest. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package stark-elliptic implements starkware elliptic curves over prime
// fields.

// This package operates, internally, on Jacobian coordinates. For a given
// (x, y) position on the curve, the Jacobian coordinates are (x1, y1, z1)
// where x = x1/z1² and y = y1/z1³. The greatest speedups come when the whole
// calculation can be performed within the transform (as in ScalarMult and
// ScalarBaseMult). But even for Add and Double, it's faster to apply and
// reverse the transform than to operate in affine coordinates.

// A StarkCurve represents a starkware Curve with a=1.
// See https://docs.starkware.co/starkex-v4/crypto/stark-curve
type StarkCurve struct {
	P       *big.Int // the order of the underlying field
	N       *big.Int // the order of the base point
	B       *big.Int // the constant of the StarkCurve equation
	Gx, Gy  *big.Int // (x,y) of the base point
	BitSize int      // the size of the underlying field
}

func (StarkCurve *StarkCurve) Params() *elliptic.CurveParams {
	return &elliptic.CurveParams{
		P:       StarkCurve.P,
		N:       StarkCurve.N,
		B:       StarkCurve.B,
		Gx:      StarkCurve.Gx,
		Gy:      StarkCurve.Gy,
		BitSize: StarkCurve.BitSize,
	}
}

// IsOnCurve returns true if the given (x,y) lies on the StarkCurve.
func (starkCurve *StarkCurve) IsOnCurve(x, y *big.Int) bool {
	// y² = x³ + x + b
	y2 := new(big.Int).Mul(y, y) //y²
	y2.Mod(y2, starkCurve.P)     //y²%P

	x3 := new(big.Int).Mul(x, x) //x²
	x3.Mul(x3, x)                //x³

	x3.Add(x3, x)            // x³ + x
	x3.Add(x3, starkCurve.B) //x³+x+B
	x3.Mod(x3, starkCurve.P) //(x³+x+B)%P

	return x3.Cmp(y2) == 0
}

// IsOnCurve returns true if the given (x,y) lies on the StarkCurve.
func (starkCurve *StarkCurve) GetYCoordinate(x *big.Int) (*big.Int, *big.Int) {
	// y² = x³ + x + b
	x3 := new(big.Int).Mul(x, x) //x²
	x3.Mul(x3, x)                //x³

	x3.Add(x3, x)            // x³ + x
	x3.Add(x3, starkCurve.B) //x³+x+B
	x3.Mod(x3, starkCurve.P) //(x³+x+B)%P

	y := big.NewInt(0).ModSqrt(x3, starkCurve.P)
	minusY := big.NewInt(0).Sub(starkCurve.P, y)
	return y, minusY
}

// Affine addition formulas: (x1,y1)+(x2,y2)=(x3,y3) where
//
//	x3 = (y2-y1)^2/(x2-x1)^2-x1-x2
//	y3 = (2*x1+x2)*(y2-y1)/(x2-x1)-(y2-y1)^3/(x2-x1)^3-y1
func (starkCurve *StarkCurve) Add(x1, y1, x2, y2 *big.Int) (*big.Int, *big.Int) {
	// support add (0,0)
	if x1.Cmp(big.NewInt(0)) == 0 && y1.Cmp(big.NewInt(0)) == 0 {
		return x2, y2
	}
	if x2.Cmp(big.NewInt(0)) == 0 && y2.Cmp(big.NewInt(0)) == 0 {
		return x1, y1
	}

	// If there is a dedicated constant-time implementation for this curve operation,
	// use that instead of the generic one.
	x3, y3 := new(big.Int), new(big.Int)
	if x1.Cmp(x2) == 0 && y1.Cmp(y2) != 0 {
		return x3, y3 // return 0, 0
	}
	if x1.Cmp(x2) == 0 && y1.Cmp(y2) == 0 {
		return starkCurve.Double(x1, y1)
	}

	y2Sub1 := new(big.Int).Sub(y2, y1)                         // (y2 -y1)
	x2Sub1 := new(big.Int).Sub(x2, x1)                         // (x2- x1)
	x2Sub1Inv := new(big.Int).ModInverse(x2Sub1, starkCurve.P) // (x2- x1)^-1
	y2Sub1Divx2Sub1 := new(big.Int).Mul(y2Sub1, x2Sub1Inv)     // (y2- y1) / (x2- x1)
	y2Sub1Divx2Sub1 = y2Sub1Divx2Sub1.Mod(y2Sub1Divx2Sub1, starkCurve.P)
	y2Sub1Divx2Sub1Pow2 := new(big.Int).Mul(y2Sub1Divx2Sub1, y2Sub1Divx2Sub1) // (y2- y1)^2 / (x2- x1)^2
	x3 = x3.Mod(y2Sub1Divx2Sub1Pow2, starkCurve.P)
	x3 = x3.Sub(x3, x1) // (y2-y1)2/(x2-x1)2 -x1
	x3 = x3.Sub(x3, x2) // (y2-y1)2/(x2-x1)2 -x1 -x2
	x3.Mod(x3, starkCurve.P)

	twoX1 := new(big.Int).Lsh(x1, 1)
	twoX1AddX2 := new(big.Int).Add(twoX1, x2)                                     // (2*x1+x2)
	y3 = y3.Mul(twoX1AddX2, y2Sub1Divx2Sub1)                                      // (2*x1+x2)*(y2-y1)/(x2-x1)
	y2Sub1Divx2Sub1Pow3 := new(big.Int).Mul(y2Sub1Divx2Sub1Pow2, y2Sub1Divx2Sub1) //  (y2-y1)^3/(x2-x1)^3
	suby3 := new(big.Int).Mod(y2Sub1Divx2Sub1Pow3, starkCurve.P)                  // (y2-y1)^3/(x2-x1)^3

	y3 = y3.Sub(y3, suby3) // (2*x1+x2)*(y2-y1)/(x2-x1)-(y2-y1)^3/(x2-x1)^3
	y3 = y3.Sub(y3, y1)    // (2*x1+x2)*(y2-y1)/(x2-x1)-(y2-y1)^3/(x2-x1)^3 - y1
	y3.Mod(y3, starkCurve.P)
	return x3, y3
}

// Affine addition formulas: (x1,y1)+(x2,y2)=(x3,y3) where
//
//	x3 = (y2-y1)^2/(x2-x1)^2-x1-x2
//	y3 = (2*x1+x2)*(y2-y1)/(x2-x1)-(y2-y1)^3/(x2-x1)^3-y1
func (starkCurve *StarkCurve) Add1(x1, y1, x2, y2 *big.Int) (*big.Int, *big.Int) {
	// support add (0,0)
	if x1.Cmp(big.NewInt(0)) == 0 && y1.Cmp(big.NewInt(0)) == 0 {
		return x2, y2
	}
	if x2.Cmp(big.NewInt(0)) == 0 && y2.Cmp(big.NewInt(0)) == 0 {
		return x1, y1
	}

	// If there is a dedicated constant-time implementation for this curve operation,
	// use that instead of the generic one.
	x3, y3 := new(big.Int), new(big.Int)
	if x1.Cmp(x2) == 0 && y1.Cmp(y2) != 0 {
		return x3, y3 // return 0, 0
	}
	if x1.Cmp(x2) == 0 && y1.Cmp(y2) == 0 {
		return starkCurve.Double(x1, y1)
	}

	y2Sub1 := new(big.Int).Sub(y2, y1)
	x2Sub1 := new(big.Int).Sub(x2, x1)
	yy2Sub1 := new(big.Int).Mul(y2Sub1, y2Sub1)
	xx2Sub1 := new(big.Int).Mul(x2Sub1, x2Sub1)
	xx2Sub1Inv := new(big.Int).ModInverse(xx2Sub1, starkCurve.P)
	x3 = x3.Mul(yy2Sub1, xx2Sub1Inv) // (y2-y1)2/(x2-x1)2
	x3 = x3.Sub(x3, x1)              // (y2-y1)2/(x2-x1)2 -x1
	x3 = x3.Sub(x3, x2)              // (y2-y1)2/(x2-x1)2 -x1 -x2
	x3.Mod(x3, starkCurve.P)

	twoX1 := new(big.Int).Lsh(x1, 1)
	twoX1AddX2 := new(big.Int).Add(twoX1, x2)                // (2*x1+x2)
	twoX1AddX2y2Sub1 := new(big.Int).Mul(twoX1AddX2, y2Sub1) // (2*x1+x2)*(y2-y1)
	x2Sub1Inv := new(big.Int).ModInverse(x2Sub1, starkCurve.P)
	y3 = y3.Mul(twoX1AddX2y2Sub1, x2Sub1Inv) // (2*x1+x2)*(y2-y1)/(x2-x1)
	yyy2Sub1 := new(big.Int).Mul(yy2Sub1, y2Sub1)
	xxx2Sub1 := new(big.Int).Mul(xx2Sub1, x2Sub1)
	xxx2Sub1Inv := new(big.Int).ModInverse(xxx2Sub1, starkCurve.P)
	suby3 := new(big.Int).Mul(yyy2Sub1, xxx2Sub1Inv) // (y2-y1)3/(x2-x1)3

	y3 = y3.Sub(y3, suby3) // (2*x1+x2)*(y2-y1)/(x2-x1)-(y2-y1)3/(x2-x1)3
	y3 = y3.Sub(y3, y1)    // (2*x1+x2)*(y2-y1)/(x2-x1)-(y2-y1)3/(x2-x1)3 - y1
	y3.Mod(y3, starkCurve.P)
	return x3, y3
}

// Affine doubling formulas: 2(x1,y1)=(x3,y3) where
//   x3 = (3*x1^2+a)^2/(2*y1)^2-x1-x1
//   y3 = (2*x1+x1)*(3*x1^2+a)/(2*y1)-(3*x1^2+a)^3/(2*y1)^3-y1

func (starkCurve *StarkCurve) Double(x1, y1 *big.Int) (*big.Int, *big.Int) {
	// If there is a dedicated constant-time implementation for this curve operation,
	// use that instead of the generic one.
	x3, y3 := new(big.Int), new(big.Int)
	xx1 := new(big.Int).Mul(x1, x1) // xx1 = x1 * x1
	threeXX1 := new(big.Int).Lsh(xx1, 1)
	threeXX1.Add(threeXX1, xx1)
	threeXX1a := new(big.Int).Add(threeXX1, big.NewInt(1)) // (3*x1^2+a)
	twoY := new(big.Int).Lsh(y1, 1)
	twoYInv := new(big.Int).ModInverse(twoY, starkCurve.P)                       // (2 * y1)^-1
	threeXX1aDivtwoY := new(big.Int).Mul(threeXX1a, twoYInv)                     // (3*x1^2+a) / (2 * y1)
	threeXX1aDivtwoYPow2 := new(big.Int).Mul(threeXX1aDivtwoY, threeXX1aDivtwoY) // (3*x12+a)^2/(2*y1)^2
	x3 = x3.Mod(threeXX1aDivtwoYPow2, starkCurve.P)                              // (3*x12+a)^2/(2*y1)^2
	twoX := new(big.Int).Lsh(x1, 1)
	x3 = x3.Sub(x3, twoX) // x3 = (3*x1^2+a)^2/(2*y1)^2-x1-x1
	x3.Mod(x3, starkCurve.P)

	threeX := new(big.Int).Add(twoX, x1)
	y3 = y3.Mul(threeX, threeXX1aDivtwoY) // (2*x1+x1)*(3*x1^2+a)/(2*y1)

	// suby3 := new(big.Int).Mul(threeXX1a2, threeXX1a)
	// twoY3 := new(big.Int).Mul(twoY2, twoY)
	// twoY3Inv := new(big.Int).ModInverse(twoY3, starkCurve.P)
	suby3 := new(big.Int).Mul(threeXX1aDivtwoY, threeXX1aDivtwoYPow2) // suby3 = (3*x1^2+a)^3/(2*y1)^3
	y3 = y3.Sub(y3, suby3)
	y3 = y3.Sub(y3, y1) //  y3 = (2*x1+x1)*(3*x12+a)/(2*y1)-(3*x12+a)3/(2*y1)3-y1
	y3.Mod(y3, starkCurve.P)
	return x3, y3
}

// Affine doubling formulas: 2(x1,y1)=(x3,y3) where
//   x3 = (3*x1^2+a)^2/(2*y1)2-x1-x1
//   y3 = (2*x1+x1)*(3*x1^2+a)/(2*y1)-(3*x1^2+a)^3/(2*y1)^3-y1

func (starkCurve *StarkCurve) Double1(x1, y1 *big.Int) (*big.Int, *big.Int) {
	// If there is a dedicated constant-time implementation for this curve operation,
	// use that instead of the generic one.
	x3, y3 := new(big.Int), new(big.Int)
	xx1 := new(big.Int).Mul(x1, x1) // xx1 = x1 * x1
	threeXX1 := new(big.Int).Lsh(xx1, 1)
	threeXX1.Add(threeXX1, xx1)
	threeXX1a := new(big.Int).Add(threeXX1, big.NewInt(1)) // (3*x12+a)
	threeXX1a2 := new(big.Int).Mul(threeXX1a, threeXX1a)   // (3*x12+a)2
	twoY := new(big.Int).Lsh(y1, 1)
	twoY2 := new(big.Int).Mul(twoY, twoY) // twoY2 = (2*y1)2
	zinv := new(big.Int).ModInverse(twoY2, starkCurve.P)
	x3 = x3.Mul(threeXX1a2, zinv) // (3*x12+a)2/(2*y1)2
	twoX := new(big.Int).Lsh(x1, 1)
	x3 = x3.Sub(x3, twoX) // x3 = (3*x12+a)2/(2*y1)2-x1-x1
	x3.Mod(x3, starkCurve.P)

	threeX := new(big.Int).Add(twoX, x1)
	twoYInv := new(big.Int).ModInverse(twoY, starkCurve.P)
	y3 = y3.Mul(threeX, threeXX1a)
	y3 = y3.Mul(y3, twoYInv) // (2*x1+x1)*(3*x12+a)/(2*y1)

	suby3 := new(big.Int).Mul(threeXX1a2, threeXX1a)
	twoY3 := new(big.Int).Mul(twoY2, twoY)
	twoY3Inv := new(big.Int).ModInverse(twoY3, starkCurve.P)
	suby3 = suby3.Mul(suby3, twoY3Inv) // suby3 = (3*x12+a)3/(2*y1)3
	y3 = y3.Sub(y3, suby3)
	y3 = y3.Sub(y3, y1) //  y3 = (2*x1+x1)*(3*x12+a)/(2*y1)-(3*x12+a)3/(2*y1)3-y1
	y3.Mod(y3, starkCurve.P)
	return x3, y3
}

/*
// Affine negation formulas: -(x1,y1)=(x1,-y1).

	func (starkCurve *StarkCurve) addJacobian(x1, y1, z1, x2, y2, z2 *big.Int) (*big.Int, *big.Int, *big.Int) {
		// See https://hyperelliptic.org/EFD/g1p/auto-shortw-jacobian-3.html#addition-add-2007-bl
		x3, y3, z3 := new(big.Int), new(big.Int), new(big.Int)
		if z1.Sign() == 0 {
			x3.Set(x2)
			y3.Set(y2)
			z3.Set(z2)
			return x3, y3, z3
		}
		if z2.Sign() == 0 {
			x3.Set(x1)
			y3.Set(y1)
			z3.Set(z1)
			return x3, y3, z3
		}

		z1z1 := new(big.Int).Mul(z1, z1)
		z1z1.Mod(z1z1, starkCurve.P)
		z2z2 := new(big.Int).Mul(z2, z2)
		z2z2.Mod(z2z2, starkCurve.P)

		u1 := new(big.Int).Mul(x1, z2z2)
		u1.Mod(u1, starkCurve.P)
		u2 := new(big.Int).Mul(x2, z1z1)
		u2.Mod(u2, starkCurve.P)
		h := new(big.Int).Sub(u2, u1)
		xEqual := h.Sign() == 0
		if h.Sign() == -1 {
			h.Add(h, starkCurve.P)
		}
		i := new(big.Int).Lsh(h, 1)
		i.Mul(i, i)
		j := new(big.Int).Mul(h, i)

		s1 := new(big.Int).Mul(y1, z2)
		s1.Mul(s1, z2z2)
		s1.Mod(s1, starkCurve.P)
		s2 := new(big.Int).Mul(y2, z1)
		s2.Mul(s2, z1z1)
		s2.Mod(s2, starkCurve.P)
		r := new(big.Int).Sub(s2, s1)
		if r.Sign() == -1 {
			r.Add(r, starkCurve.P)
		}
		yEqual := r.Sign() == 0
		if xEqual && yEqual {
			//return starkCurve.doubleJacobian(x1, y1, z1)
		}
		r.Lsh(r, 1)
		v := new(big.Int).Mul(u1, i)

		x3.Set(r)
		x3.Mul(x3, x3)
		x3.Sub(x3, j)
		x3.Sub(x3, v)
		x3.Sub(x3, v)
		x3.Mod(x3, starkCurve.P)

		y3.Set(r)
		v.Sub(v, x3)
		y3.Mul(y3, v)
		s1.Mul(s1, j)
		s1.Lsh(s1, 1)
		y3.Sub(y3, s1)
		y3.Mod(y3, starkCurve.P)

		z3.Add(z1, z2)
		z3.Mul(z3, z3)
		z3.Sub(z3, z1z1)
		z3.Sub(z3, z2z2)
		z3.Mul(z3, h)
		z3.Mod(z3, starkCurve.P)

		return x3, y3, z3
	}
*/
func (starkCurve *StarkCurve) ScalarMult(Bx, By *big.Int, k []byte) (*big.Int, *big.Int) {
	// If there is a dedicated constant-time implementation for this curve operation,
	// use that instead of the generic one.

	x := Bx
	y := By

	seenFirstTrue := false
	for _, byteData := range k {
		for bitNum := 0; bitNum < 8; bitNum++ {
			if seenFirstTrue {
				x, y = starkCurve.Double(x, y)
			}
			if byteData&0x80 == 0x80 {
				if !seenFirstTrue {
					seenFirstTrue = true
				} else {
					x, y = starkCurve.Add(Bx, By, x, y)
				}
			}
			byteData <<= 1
		}
	}

	if !seenFirstTrue {
		return nil, nil
	}
	return x, y
}

func (starkCurve *StarkCurve) ScalarBaseMult(k []byte) (*big.Int, *big.Int) {
	return starkCurve.ScalarMult(starkCurve.Gx, starkCurve.Gy, k)
}

//  一个优化点可以计算 g 2g 4g 8g ....的值，然后用add方法来计算，理论上可以减少计算量，进行一定的计算
// An optimization point can calculate the values of g 2g 4g 8g...., and then use the add method to calculate, which theoretically can reduce the amount of calculation
func (starkCurve *StarkCurve) ScalarBaseMultV2(k []byte) (*big.Int, *big.Int) {
	seenFirstTrue := false
	firstPos := len(k)*8 - 1
	var x *big.Int
	var y *big.Int
	for _, byteData := range k {
		for bitNum := 0; bitNum < 8; bitNum++ {
			if byteData&0x80 == 0x80 {
				if !seenFirstTrue {
					seenFirstTrue = true
					x = big.NewInt(0).Set(constPoints.ConstantPoints[firstPos][0])
					y = big.NewInt(0).Set(constPoints.ConstantPoints[firstPos][1])
				} else {
					tempx := big.NewInt(0).Set(constPoints.ConstantPoints[firstPos][0])
					tempy := big.NewInt(0).Set(constPoints.ConstantPoints[firstPos][1])
					x, y = starkCurve.Add(tempx, tempy, x, y)
				}
			}
			byteData <<= 1
			firstPos--
		}
	}

	if !seenFirstTrue {
		return nil, nil
	}
	return x, y
}

var mask = []byte{0xff, 0x1, 0x3, 0x7, 0xf, 0x1f, 0x3f, 0x7f}

// TODO: double check if it is okay
// GenerateKey returns a public/private key pair. The private key is generated
// using the given reader, which must return random data.
func (starkCurve *StarkCurve) GenerateKey(rand io.Reader) (priv []byte, x, y *big.Int, err error) {
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
		x, y = starkCurve.ScalarBaseMult(priv)
	}
	return
}

// Marshal converts a point into the form specified in section 4.3.6 of ANSI
// X9.62.
func (starkCurve *StarkCurve) Marshal(x, y *big.Int) []byte {
	byteLen := (starkCurve.BitSize + 7) >> 3

	ret := make([]byte, 1+2*byteLen)
	ret[0] = 4 // uncompressed point

	xBytes := x.Bytes()
	copy(ret[1+byteLen-len(xBytes):], xBytes)
	yBytes := y.Bytes()
	copy(ret[1+2*byteLen-len(yBytes):], yBytes)
	return ret
}

// Unmarshal converts a point, serialised by Marshal, into an x, y pair. On
// error, x = nil.
func (starkCurve *StarkCurve) Unmarshal(data []byte) (x, y *big.Int) {
	byteLen := (starkCurve.BitSize + 7) >> 3
	if len(data) != 1+2*byteLen {
		return
	}
	if data[0] != 4 { // uncompressed form
		return
	}
	x = new(big.Int).SetBytes(data[1 : 1+byteLen])
	y = new(big.Int).SetBytes(data[1+byteLen:])
	return
}

func NewStarkCurve() *StarkCurve {
	p, valid := big.NewInt(0).SetString("800000000000011000000000000000000000000000000000000000000000001", 16)
	if !valid {
		fmt.Println("invalid p")
		return nil
	}
	n, valid := big.NewInt(0).SetString("0800000000000010ffffffffffffffffb781126dcae7b2321e66a241adc64d2f", 16)
	if !valid {
		fmt.Println("invalid n")
		return nil
	}
	b, valid := big.NewInt(0).SetString("06f21413efbe40de150e596d72f7a8c5609ad26c15c915c1f4cdfcb99cee9e89", 16)
	if !valid {
		fmt.Println("invalid b")
		return nil
	}
	gx, valid := big.NewInt(0).SetString("1ef15c18599971b7beced415a40f0c7deacfd9b0d1819e03d723d8bc943cfca", 16)
	if !valid {
		fmt.Println("invalid gx")
		return nil
	}
	gy, valid := big.NewInt(0).SetString("5668060aa49730b7be4801df46ec62de53ecd11abe43a32873000c36e8dc1f", 16)
	if !valid {
		fmt.Println("invalid gy")
		return nil
	}
	return &StarkCurve{
		P:       p,
		N:       n,
		B:       b,
		Gx:      gx,
		Gy:      gy,
		BitSize: 256,
	}
}
