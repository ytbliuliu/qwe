package ezp

import (
	"encoding/binary"
	"math/big"
	"math/rand"
	"time"

	"github.com/ethereum/go-ethereum/crypto/sha3"
	"github.com/ethereum/go-ethereum/ethutil"
	"github.com/ethereum/go-ethereum/logger"
	"github.com/ethereum/go-ethereum/pow"
)

var powlogger = logger.NewLogger("POW")

type EasyPow struct {
	hash     *big.Int
	HashRate int64
	turbo    bool
}

func New() *EasyPow {
	return &EasyPow{turbo: false}
}

func (pow *EasyPow) GetHashrate() int64 {
	return pow.HashRate
}

func (pow *EasyPow) Turbo(on bool) {
	pow.turbo = on
}

func (pow *EasyPow) Search(block pow.Block, stop <-chan struct{}) (uint64, []byte, []byte) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	hash := block.HashNoNonce()
	diff := block.Difficulty()
	//i := int64(0)
	// TODO fix offset
	i := rand.Int63()
	starti := i
	start := time.Now().UnixNano()

	defer func() { pow.HashRate = 0 }()

	// Make sure stop is empty
empty:
	for {
		select {
		case <-stop:
		default:
			break empty
		}
	}

	for {
		select {
		case <-stop:
			return 0, nil, nil
		default:
			i++

			elapsed := time.Now().UnixNano() - start
			hashes := ((float64(1e9) / float64(elapsed)) * float64(i-starti)) / 1000
			pow.HashRate = int64(hashes)

			nonce := uint64(r.Int63())
			if verify(hash, diff, nonce) {
				return nonce, nil, nil
			}
		}

		if !pow.turbo {
			time.Sleep(20 * time.Microsecond)
		}
	}
}

func (pow *EasyPow) Verify(block pow.Block) bool {
	return Verify(block)
}

func verify(hash []byte, diff *big.Int, nonce uint64) bool {
	sha := sha3.NewKeccak256()

	nonce_buf := make([]byte, 8)
	binary.PutUvarint(nonce_buf, nonce)
	d := append(hash, nonce_buf...)
	sha.Write(d)

	verification := new(big.Int).Div(ethutil.BigPow(2, 256), diff)
	res := ethutil.BigD(sha.Sum(nil))

	return res.Cmp(verification) <= 0
}

func Verify(block pow.Block) bool {
	return verify(block.HashNoNonce(), block.Difficulty(), block.Nonce())
}
