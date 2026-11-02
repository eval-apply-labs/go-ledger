package signing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"math/big"
	"slices"
	"strings"
	"testing"
)

// The tests of ed25519.go's ValidatePublicKey. They run only when run.sh swaps ed25519.go in.

// edwardsPoint is a point (x, y) of the curve -x^2 + y^2 = 1 + d x^2 y^2 mod p, for the checks below,
// which do the curve's arithmetic with math/big, apart from crypto/ed25519.
type edwardsPoint struct{ x, y *big.Int }

// fieldMultiply is the product of values mod p.
func fieldMultiply(values ...*big.Int) *big.Int {
	product := big.NewInt(1)
	for _, value := range values {
		product.Mod(product.Mul(product, value), fieldPrime)
	}
	return product
}

// fieldDivide is numerator / denominator mod p.
func fieldDivide(numerator, denominator *big.Int) *big.Int {
	inverse := new(big.Int).ModInverse(new(big.Int).Mod(denominator, fieldPrime), fieldPrime)
	return fieldMultiply(numerator, inverse)
}

// addPoints is the Edwards addition law of RFC 8032, section 5.1.4, which also doubles a point.
func addPoints(first, second edwardsPoint) edwardsPoint {
	dxxyy := fieldMultiply(curveD, first.x, second.x, first.y, second.y)
	x := fieldDivide(new(big.Int).Add(fieldMultiply(first.x, second.y), fieldMultiply(first.y, second.x)), new(big.Int).Add(big.NewInt(1), dxxyy))
	y := fieldDivide(new(big.Int).Add(fieldMultiply(first.y, second.y), fieldMultiply(first.x, second.x)), new(big.Int).Sub(big.NewInt(1), dxxyy))
	return edwardsPoint{x, y}
}

// decodePoint decodes an encoded point as RFC 8032, section 5.1.3, does: false for a y of p or above,
// a y no point has, or x = 0 with the sign bit set.
func decodePoint(encoded []byte) (edwardsPoint, bool) {
	y := yCoordinate(encoded)
	if y.Cmp(fieldPrime) >= 0 {
		return edwardsPoint{}, false
	}
	ySquared := fieldMultiply(y, y)
	xSquared := fieldDivide(new(big.Int).Sub(ySquared, big.NewInt(1)), new(big.Int).Add(fieldMultiply(curveD, ySquared), big.NewInt(1)))
	x := new(big.Int).ModSqrt(xSquared, fieldPrime)
	signBit := uint(encoded[31] >> 7)
	if x == nil || (x.Sign() == 0 && signBit == 1) {
		return edwardsPoint{}, false
	}
	if x.Bit(0) != signBit {
		x.Sub(fieldPrime, x)
	}
	return edwardsPoint{x, y}, true
}

func onTheCurve(point edwardsPoint) bool {
	xSquared, ySquared := fieldMultiply(point.x, point.x), fieldMultiply(point.y, point.y)
	left := new(big.Int).Mod(new(big.Int).Sub(ySquared, xSquared), fieldPrime)
	right := new(big.Int).Mod(new(big.Int).Add(big.NewInt(1), fieldMultiply(curveD, xSquared, ySquared)), fieldPrime)
	return left.Cmp(right) == 0
}

func isIdentity(point edwardsPoint) bool {
	return point.x.Sign() == 0 && point.y.Cmp(big.NewInt(1)) == 0
}

// withSignBit is encoded with its top bit, the sign of x, set to signBit.
func withSignBit(encoded []byte, signBit byte) []byte {
	copied := bytes.Clone(encoded)
	copied[31] = copied[31]&0x7f | signBit<<7
	return copied
}

// encodeY is the 32-byte little-endian encoding of y, the sign bit clear.
func encodeY(y *big.Int) []byte {
	encoded := y.FillBytes(make([]byte, 32))
	for i, j := 0, 31; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return encoded
}

// The constants of ed25519.go, checked apart from crypto/ed25519: d is RFC 8032's; d y^2 + 1 is never
// 0, as ValidatePublicKey's curve check assumes; and the y values in smallOrderYs, with either sign
// bit, give eight distinct points of the curve, each with 8P the identity. Ed25519's group has 8L
// points, L prime, so the points of order dividing 8 are a subgroup of exactly eight: these. The base
// point, of order L, is the counter-check that the arithmetic does not make every 8P the identity.
func TestEd25519SmallOrderConstants(t *testing.T) {
	if want := "37095705934669439343138083508754565189542113879843219016388785533085940283555"; curveD.String() != want {
		t.Fatalf("d is %s, want RFC 8032's %s", curveD, want)
	}
	if minusInverseD := new(big.Int).Sub(fieldPrime, new(big.Int).ModInverse(curveD, fieldPrime)); big.Jacobi(minusInverseD, fieldPrime) != -1 {
		t.Fatal("-1/d is a square mod p, so d y^2 + 1 can be 0")
	}
	timesEight := func(point edwardsPoint) edwardsPoint {
		for range 3 {
			point = addPoints(point, point)
		}
		return point
	}
	distinct := map[string]bool{}
	for _, smallOrderY := range smallOrderYs {
		for signBit := range byte(2) {
			point, decoded := decodePoint(withSignBit(encodeY(smallOrderY), signBit))
			if !decoded {
				continue // x = 0 (y = 1 or p - 1): only the clear sign bit encodes the point
			}
			if !onTheCurve(point) || !isIdentity(timesEight(point)) {
				t.Errorf("y = %x with sign bit %d is not on the curve with 8P the identity", encodeY(smallOrderY), signBit)
			}
			distinct[point.x.String()+","+point.y.String()] = true
		}
	}
	if len(distinct) != 8 {
		t.Errorf("the small-order constants give %d distinct points, want the 8 of order 1, 2, 4 and 8", len(distinct))
	}
	basePoint, decoded := decodePoint(mustDecodeHex("5866666666666666666666666666666666666666666666666666666666666666"))
	if !decoded || !onTheCurve(basePoint) || isIdentity(timesEight(basePoint)) {
		t.Error("the base point does not decode to a point of the curve with 8B not the identity")
	}
}

// ValidatePublicKey refuses, with the reason: a key of another size; each encoding of a point of small
// order, either sign bit; every y of p or above, the 19 values up to 2^255 - 1, either sign bit; and a
// y no point has. ValidatePublicKeyBase64, the check every key that enters the ledger passes, refuses
// them too.
func TestEd25519ValidatePublicKeyRefusesDegenerateKeys(t *testing.T) {
	scheme := NetworkScheme()
	refusedFor := func(publicKey []byte, reason string) {
		t.Helper()
		if err := scheme.ValidatePublicKey(publicKey); err == nil || !strings.Contains(err.Error(), reason) {
			t.Errorf("the key %x was refused with %v, want %q", publicKey, err, reason)
		}
		if ValidatePublicKeyBase64(scheme, base64.StdEncoding.EncodeToString(publicKey)) == nil {
			t.Errorf("the key %x passed ValidatePublicKeyBase64", publicKey)
		}
	}
	for _, size := range []int{0, 31, 33, 64, 1952} {
		refusedFor(make([]byte, size), "a public key is 32 bytes")
	}
	for _, smallOrderY := range smallOrderYs {
		for signBit := range byte(2) {
			refusedFor(withSignBit(encodeY(smallOrderY), signBit), "small order")
		}
	}
	for offset := range int64(19) {
		y := new(big.Int).Add(fieldPrime, big.NewInt(offset))
		for signBit := range byte(2) {
			refusedFor(withSignBit(encodeY(y), signBit), "another encoding")
		}
	}
	if _, decoded := decodePoint(encodeY(big.NewInt(2))); decoded {
		t.Fatal("premise: no point has y = 2")
	}
	refusedFor(encodeY(big.NewInt(2)), "no point of the curve")
}

// ValidatePublicKey takes genuine keys: RFC 8032's first test key, the base point, and a thousand fresh
// ones, each of which verifies its own signature.
func TestEd25519ValidatePublicKeyTakesGenuineKeys(t *testing.T) {
	scheme := NetworkScheme()
	for _, publicKey := range [][]byte{
		mustDecodeHex("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"),
		mustDecodeHex("5866666666666666666666666666666666666666666666666666666666666666"),
	} {
		if err := scheme.ValidatePublicKey(publicKey); err != nil {
			t.Errorf("the key %x was refused: %v", publicKey, err)
		}
	}
	for range 1000 {
		publicKey, seed, err := scheme.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		privateKey, _ := scheme.ParsePrivateKey(seed)
		signature, _ := privateKey.Sign([]byte("x"))
		if err := scheme.ValidatePublicKey(publicKey); err != nil || !scheme.Verify(publicKey, []byte("x"), signature) {
			t.Fatalf("the fresh key %x was refused (%v), or does not verify its signature", publicKey, err)
		}
	}
}

// Why the scheme's Verify asks ValidatePublicKey first: under a key of small order, the signature R =
// the identity, S = 0 verifies in crypto/ed25519 for every message whose hash k times the key is the
// identity, one in eight at worst, and for every message under the identity. That holds for the
// encodings of y of p or above too. The scheme's Verify refuses each.
func TestEd25519VerifyRefusesWhatCryptoEd25519Accepts(t *testing.T) {
	scheme := NetworkScheme()
	identity := mustDecodeHex("0100000000000000000000000000000000000000000000000000000000000000")
	forgedSignature := append(bytes.Clone(identity), make([]byte, 32)...)
	weakKeys := [][]byte{}
	for _, y := range append(slices.Clone(smallOrderYs), fieldPrime, new(big.Int).Add(fieldPrime, big.NewInt(1))) { // p and p + 1: y = 0 and y = 1 again
		for signBit := range byte(2) {
			weakKeys = append(weakKeys, withSignBit(encodeY(y), signBit))
		}
	}
	for _, weakKey := range weakKeys {
		forged, verifiedByTheScheme := 0, 0
		for i := range 256 {
			message := []byte{byte(i)}
			if ed25519.Verify(weakKey, message, forgedSignature) {
				forged++
				if scheme.Verify(weakKey, message, forgedSignature) {
					verifiedByTheScheme++
				}
			}
		}
		if forged == 0 || verifiedByTheScheme > 0 {
			t.Errorf("under the weak key %x, crypto/ed25519 took the forged signature for %d of 256 messages, want some, and the scheme for %d, want none",
				weakKey, forged, verifiedByTheScheme)
		}
	}
}
