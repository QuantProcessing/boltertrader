package starkcurve

import (
	"math/big"
)

const (
	compactBits      = 8
	pedersenHashBits = 252
)

type HashPoint struct {
	X *big.Int
	Y *big.Int
}

var (
	hashParams1 []HashPoint
	hashParams2 []HashPoint
)

// get max bit size and remainer
func splitInt(x int) (int, int) {
	if x == 0 {
		return 0, 0
	}

	highestBitIdx := 0
	v := x
	for v > 0 {
		highestBitIdx++
		v >>= 1
	}
	return highestBitIdx, x - (1 << (highestBitIdx - 1))

}

func InitHashParams() {
	hashParams1 = make([]HashPoint, (pedersenHashBits/compactBits+1)*(1<<compactBits))
	hashParams2 = make([]HashPoint, (pedersenHashBits/compactBits+1)*(1<<compactBits))
	curve := NewStarkCurve()
	for i := 0; i < (pedersenHashBits/compactBits+1)*(1<<compactBits); i++ {
		// hashParams1[i].X = big.NewInt(0)
		// hashParams1[i].Y = big.NewInt(0)
		// hashParams2[i].X = big.NewInt(0)
		// hashParams2[i].Y = big.NewInt(0)
	}
	for i := 0; i < (pedersenHashBits+compactBits-1)/compactBits; i++ {
		for j := 0; j < (1 << compactBits); j++ {
			v := j
			highestBitIdx, remainder := splitInt(v)
			idx := i * compactBits
			var points []*big.Int
			if idx+highestBitIdx+1 < 253 {
				points = cfg.ConstantPoints[idx+highestBitIdx+1]
			} else {
				points = []*big.Int{big.NewInt(0), big.NewInt(0)}
			}

			x := big.NewInt(0).Set(points[0])
			y := big.NewInt(0).Set(points[1])
			var points2 []*big.Int
			if idx+highestBitIdx+1 < 253 {
				points2 = cfg.ConstantPoints[pedersenHashBits+idx+highestBitIdx+1]
			} else {
				points2 = []*big.Int{big.NewInt(0), big.NewInt(0)}
			}

			x2 := big.NewInt(0).Set(points2[0])
			y2 := big.NewInt(0).Set(points2[1])
			if remainder == 0 {

				hashParams1[i<<compactBits+j] = HashPoint{
					X: x,
					Y: y,
				}

				hashParams2[i<<compactBits+j] = HashPoint{
					X: x2,
					Y: y2,
				}
			} else {
				x3 := hashParams1[i<<compactBits+remainder].X
				y3 := hashParams1[i<<compactBits+remainder].Y
				x4 := hashParams2[i<<compactBits+remainder].X
				y4 := hashParams2[i<<compactBits+remainder].Y
				x5, y5 := curve.Add(x3, y3, x, y)
				x6, y6 := curve.Add(x4, y4, x2, y2)
				hashParams1[i<<compactBits+j] = HashPoint{
					X: x5,
					Y: y5,
				}

				hashParams2[i<<compactBits+j] = HashPoint{
					X: x6,
					Y: y6,
				}
			}
		}

	}
}

// def pedersen_hash(*elements: int) -> int:
//     return pedersen_hash_as_point(*elements)[0]

// def pedersen_hash_as_point(*elements: int) -> ECPoint:
//     """
//     Similar to pedersen_hash but also returns the y coordinate of the resulting EC point.
//     This function is used for testing.
//     """
//     point = SHIFT_POINT
//     for i, x in enumerate(elements):
//         assert 0 <= x < FIELD_PRIME
//         point_list = CONSTANT_POINTS[2 + i * N_ELEMENT_BITS_HASH:2 + (i + 1) * N_ELEMENT_BITS_HASH]
//         assert len(point_list) == N_ELEMENT_BITS_HASH
//         for pt in point_list:
//             assert point[0] != pt[0], 'Unhashable input.'
//             if x & 1:
//                 point = ec_add(point, pt, FIELD_PRIME)
//             x >>= 1
//         assert x == 0
//     return point

func FastHash(x *big.Int, y *big.Int) []byte {
	if x.Cmp(cfg.FieldPrime) >= 0 {
		return nil
	}
	if y.Cmp(cfg.FieldPrime) >= 0 {
		return nil
	}
	shiftPointx := big.NewInt(0).Set(cfg.ConstantPoints[0][0])
	shiftPointy := big.NewInt(0).Set(cfg.ConstantPoints[0][1])
	//fmt.Printf("shiftPointx %x, shiftPointy %x\n", shiftPointx.Bytes(), shiftPointy.Bytes())
	//fmt.Printf("shiftPointx %v, shiftPointy %v\n", shiftPointx.String(), shiftPointy.String())
	curve := NewStarkCurve()

	x1 := big.NewInt(0).Set(x)
	y1 := big.NewInt(0).Set(y)

	mask := big.NewInt(1<<compactBits - 1)
	// add x1
	for i := 0; i < (pedersenHashBits+compactBits-1)/compactBits; i++ {
		maskResult := big.NewInt(0).And(x1, mask).Int64()
		if maskResult > 0 {
			pos := i*1<<compactBits + int(maskResult)
			hashVal := hashParams1[pos]
			// fmt.Printf("pos %v, %#x\n", pos, hashVal)
			shiftPointx, shiftPointy = curve.Add(shiftPointx, shiftPointy, hashVal.X, hashVal.Y)
		}
		x1 = big.NewInt(0).Rsh(x1, compactBits)
	}

	for i := 0; i < (pedersenHashBits+compactBits-1)/compactBits; i++ {
		maskResult := big.NewInt(0).And(y1, mask).Int64()
		if maskResult > 0 {
			hashVal := hashParams2[i*1<<compactBits+int(maskResult)]
			shiftPointx, shiftPointy = curve.Add(shiftPointx, shiftPointy, hashVal.X, hashVal.Y)
		}
		y1 = big.NewInt(0).Rsh(y1, compactBits)
	}
	return shiftPointx.Bytes()
}
