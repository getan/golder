package adb

import (
	"encoding/hex"
	"math/big"
)

var (
	fieldP     = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	groupL     = new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 252), mustBig("27742317777372353535851937790883648493"))
	curveD     = new(big.Int).Mod(new(big.Int).Mul(big.NewInt(-121665), modInverse(big.NewInt(121666), fieldP)), fieldP)
	curveD2    = new(big.Int).Mod(new(big.Int).Mul(curveD, big.NewInt(2)), fieldP)
	sqrtMinus1 = new(big.Int).Exp(big.NewInt(2), new(big.Int).Rsh(new(big.Int).Sub(fieldP, big.NewInt(1)), 2), fieldP)
)

type edPoint struct {
	x, y, z, t *big.Int
}

var (
	edIdentity = &edPoint{big.NewInt(0), big.NewInt(1), big.NewInt(1), big.NewInt(0)}
	edBase     = mustDecodeEd("5866666666666666666666666666666666666666666666666666666666666666")
	spakeM     = mustDecodeEd("5ada7e4bf6ddd9adb6626d32131c6b5c51a1e347a3478f53cfcf441b88eed12e")
	spakeN     = mustDecodeEd("10e3df0ae37d8e7a99b5fe74b44672103dbddcbd06af680d71329a11693bc778")
)

func mustBig(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("invalid big integer " + s)
	}
	return n
}

func modP(v *big.Int) *big.Int {
	v.Mod(v, fieldP)
	if v.Sign() < 0 {
		v.Add(v, fieldP)
	}
	return v
}

func mulMod(a, b *big.Int) *big.Int {
	return modP(new(big.Int).Mul(a, b))
}

func modInverse(a, m *big.Int) *big.Int {
	return new(big.Int).ModInverse(a, m)
}

func (p *edPoint) add(q *edPoint) *edPoint {
	a := mulMod(new(big.Int).Sub(p.y, p.x), new(big.Int).Sub(q.y, q.x))
	b := mulMod(new(big.Int).Add(p.y, p.x), new(big.Int).Add(q.y, q.x))
	c := mulMod(mulMod(p.t, curveD2), q.t)
	d := mulMod(mulMod(p.z, big.NewInt(2)), q.z)
	e := new(big.Int).Sub(b, a)
	f := new(big.Int).Sub(d, c)
	g := new(big.Int).Add(d, c)
	h := new(big.Int).Add(b, a)
	return &edPoint{
		x: mulMod(e, f),
		y: mulMod(g, h),
		z: mulMod(f, g),
		t: mulMod(e, h),
	}
}

func (p *edPoint) neg() *edPoint {
	return &edPoint{
		x: modP(new(big.Int).Neg(p.x)),
		y: new(big.Int).Set(p.y),
		z: new(big.Int).Set(p.z),
		t: modP(new(big.Int).Neg(p.t)),
	}
}

func (p *edPoint) sub(q *edPoint) *edPoint {
	return p.add(q.neg())
}

func (p *edPoint) times(k *big.Int) *edPoint {
	acc := &edPoint{big.NewInt(0), big.NewInt(1), big.NewInt(1), big.NewInt(0)}
	for i := k.BitLen() - 1; i >= 0; i-- {
		acc = acc.add(acc)
		if k.Bit(i) == 1 {
			acc = acc.add(p)
		}
	}
	return acc
}

func (p *edPoint) affine() (*big.Int, *big.Int) {
	zi := modInverse(modP(new(big.Int).Set(p.z)), fieldP)
	return mulMod(p.x, zi), mulMod(p.y, zi)
}

func (p *edPoint) encode() []byte {
	x, y := p.affine()
	out := toLE(y, 32)
	if x.Bit(0) == 1 {
		out[31] |= 0x80
	}
	return out
}

func decodeEd(b []byte) *edPoint {
	if len(b) != 32 {
		return nil
	}
	raw := append([]byte(nil), b...)
	sign := raw[31] >> 7
	raw[31] &= 0x7f
	y := fromLE(raw)
	if y.Cmp(fieldP) >= 0 {
		return nil
	}
	yy := mulMod(y, y)
	u := modP(new(big.Int).Sub(yy, big.NewInt(1)))
	v := modP(new(big.Int).Add(mulMod(curveD, yy), big.NewInt(1)))
	v3 := mulMod(mulMod(v, v), v)
	v7 := mulMod(mulMod(v3, v3), v)
	x := mulMod(mulMod(u, v3), new(big.Int).Exp(mulMod(u, v7), new(big.Int).Rsh(new(big.Int).Sub(fieldP, big.NewInt(5)), 3), fieldP))
	vxx := mulMod(mulMod(v, x), x)
	switch {
	case vxx.Cmp(u) == 0:
	case vxx.Cmp(modP(new(big.Int).Neg(u))) == 0:
		x = mulMod(x, sqrtMinus1)
	default:
		return nil
	}
	if x.Sign() == 0 && sign == 1 {
		return nil
	}
	if x.Bit(0) != uint(sign) {
		x = modP(new(big.Int).Neg(x))
	}
	return &edPoint{x, y, big.NewInt(1), mulMod(x, y)}
}

func fromLE(b []byte) *big.Int {
	rev := append([]byte(nil), b...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return new(big.Int).SetBytes(rev)
}

func toLE(n *big.Int, size int) []byte {
	out := make([]byte, size)
	be := n.Bytes()
	for i := 0; i < len(be) && i < size; i++ {
		out[i] = be[len(be)-1-i]
	}
	return out
}

func mustDecodeEd(s string) *edPoint {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	p := decodeEd(b)
	if p == nil {
		panic("invalid edwards point")
	}
	return p
}
