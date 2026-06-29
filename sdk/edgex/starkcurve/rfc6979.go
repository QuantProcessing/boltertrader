/*
Package rfc6979 is an implementation of RFC 6979's deterministic DSA.
	Such signatures are compatible with standard Digital Signature Algorithm
	(DSA) and Elliptic Curve Digital Signature Algorithm (ECDSA) digital
	signatures and can be processed with unmodified verifiers, which need not be
	aware of the procedure described therein.  Deterministic signatures retain
	the cryptographic security features associated with digital signatures but
	can be more easily implemented in various environments, since they do not
	need access to a source of high-quality randomness.
(https://tools.ietf.org/html/rfc6979)
Provides functions similar to crypto/dsa and crypto/ecdsa.
*/

package starkcurve

import (
	"crypto/hmac"
	"hash"
	"math/big"
	"math/rand"
	"time"
)

// mac returns an HMAC of the given key and message.
func mac(alg func() hash.Hash, k, m, buf []byte) []byte {
	h := hmac.New(alg, k)
	h.Write(m)
	return h.Sum(buf[:0])
}

// https://tools.ietf.org/html/rfc6979#section-2.3.2
func bits2int(in []byte, qlen int) *big.Int {
	vlen := len(in) * 8
	v := new(big.Int).SetBytes(in)
	if vlen > qlen {
		v = new(big.Int).Rsh(v, uint(vlen-qlen))
	}
	return v
}

// https://tools.ietf.org/html/rfc6979#section-2.3.3
func int2octets(v *big.Int, rolen int) []byte {
	out := v.Bytes()

	// pad with zeros if it's too short
	if len(out) < rolen {
		out2 := make([]byte, rolen)
		copy(out2[rolen-len(out):], out)
		return out2
	}

	// drop most significant bytes if it's too long
	if len(out) > rolen {
		out2 := make([]byte, rolen)
		copy(out2, out[len(out)-rolen:])
		return out2
	}

	return out
}

// https://tools.ietf.org/html/rfc6979#section-2.3.4
func bits2octets(in []byte, q *big.Int, qlen, rolen int) []byte {
	z1 := bits2int(in, qlen)
	z2 := new(big.Int).Sub(z1, q)
	if z2.Sign() < 0 {
		return int2octets(z1, rolen)
	}
	return int2octets(z2, rolen)
}

var one = big.NewInt(1)

// https://tools.ietf.org/html/rfc6979#section-3.2
func generateSecret(q *big.Int, alg func() hash.Hash, hashOrig []byte, test func(*big.Int) bool) {

	// if 1 <= msg_hash.bit_length() % 8 <= 4 and msg_hash.bit_length() >= 248:
	// # Only if we are one-nibble short:
	// msg_hash *= 16
	msg_hashInt := big.NewInt(0).SetBytes(hashOrig)
	msg_hashBits := msg_hashInt.BitLen()
	if msg_hashBits >= 248 && msg_hashBits%8 >= 1 && msg_hashBits%8 < 4 {
		msg_hashInt = msg_hashInt.Mul(msg_hashInt, big.NewInt(16))
	}
	hash := msg_hashInt.Bytes()
	seed := time.Now().Unix()
	rand.Seed(seed)
	randValue := rand.Int()

	qlen := q.BitLen()
	// Step H
	for {
		var t []byte
		t = mac(alg, hash, big.NewInt(int64(randValue)).Bytes(), t)
		// Step H3
		secret := bits2int(t, qlen)
		if secret.Cmp(one) >= 0 && secret.Cmp(q) < 0 && test(secret) {
			return
		}
		randValue++
	}
}
