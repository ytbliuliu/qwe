package ethash

import (
	"encoding/json"
	"io/ioutil"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Tests whether remote HTTP servers are correctly notified of new work.
func TestRemoteNotify(t *testing.T) {
	// Start a simple webserver to capture notifications
	sink := make(chan [3]string)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			blob, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("failed to read miner notification: %v", err)
			}
			var work [3]string
			if err := json.Unmarshal(blob, &work); err != nil {
				t.Fatalf("failed to unmarshal miner notification: %v", err)
			}
			sink <- work
		}),
	}
	// Open a custom listener to extract its local address
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to open notification server: %v", err)
	}
	defer listener.Close()

	go server.Serve(listener)

	// Create the custom ethash engine
	ethash := NewTester([]string{"http://" + listener.Addr().String()})
	defer ethash.Close()

	// Stream a work task and ensure the notification bubbles out
	header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(100)}
	block := types.NewBlockWithHeader(header)

	ethash.Seal(nil, block, nil)
	select {
	case work := <-sink:
		if want := ethash.SealHash(header).Hex(); work[0] != want {
			t.Errorf("work packet hash mismatch: have %s, want %s", work[0], want)
		}
		if want := common.BytesToHash(SeedHash(header.Number.Uint64())).Hex(); work[1] != want {
			t.Errorf("work packet seed mismatch: have %s, want %s", work[1], want)
		}
		target := new(big.Int).Div(new(big.Int).Lsh(big.NewInt(1), 256), header.Difficulty)
		if want := common.BytesToHash(target.Bytes()).Hex(); work[2] != want {
			t.Errorf("work packet target mismatch: have %s, want %s", work[2], want)
		}
	case <-time.After(time.Second):
		t.Fatalf("notification timed out")
	}
}

// Tests that pushing work packages fast to the miner doesn't cause any data race
// issues in the notifications.
func TestRemoteMultiNotify(t *testing.T) {
	// Start a simple webserver to capture notifications
	sink := make(chan [3]string, 64)

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			blob, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("failed to read miner notification: %v", err)
			}
			var work [3]string
			if err := json.Unmarshal(blob, &work); err != nil {
				t.Fatalf("failed to unmarshal miner notification: %v", err)
			}
			sink <- work
		}),
	}
	// Open a custom listener to extract its local address
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to open notification server: %v", err)
	}
	defer listener.Close()

	go server.Serve(listener)

	// Create the custom ethash engine
	ethash := NewTester([]string{"http://" + listener.Addr().String()})
	defer ethash.Close()

	// Stream a lot of work task and ensure all the notifications bubble out
	for i := 0; i < cap(sink); i++ {
		header := &types.Header{Number: big.NewInt(int64(i)), Difficulty: big.NewInt(100)}
		block := types.NewBlockWithHeader(header)

		ethash.Seal(nil, block, nil)
	}
	for i := 0; i < cap(sink); i++ {
		select {
		case <-sink:
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("notification %d timed out", i)
		}
	}
}

// Tests whether stale solutions are correctly processed.
func TestStaleSubmission(t *testing.T) {
	ethash := NewTester(nil)
	defer ethash.Close()
	ethash.disableRemoteVerify = true
	api := &API{ethash}

	fakeNonce, fakeDigest := types.BlockNonce{0x01, 0x02, 0x03}, common.HexToHash("deadbeef")

	testcases := []struct {
		headers     []*types.Header
		resCh       chan *types.Block
		submitIndex int
		submitRes   bool
	}{
		// Case1: submit solution for the latest mining package
		{
			[]*types.Header{
				{ParentHash: common.BytesToHash([]byte{0xa}), Number: big.NewInt(1), Difficulty: big.NewInt(100)},
			},
			ethash.resultCh,
			0,
			true,
		},
		// Case2: submit solution for the previous package but have same parent.
		{
			[]*types.Header{
				{ParentHash: common.BytesToHash([]byte{0xb}), Number: big.NewInt(2), Difficulty: big.NewInt(100)},
				{ParentHash: common.BytesToHash([]byte{0xb}), Number: big.NewInt(2), Difficulty: big.NewInt(101)},
			},
			ethash.resultCh,
			0,
			true,
		},
		// Case3: submit stale but acceptable solution
		{
			[]*types.Header{
				{ParentHash: common.BytesToHash([]byte{0xc}), Number: big.NewInt(3), Difficulty: big.NewInt(100)},
				{ParentHash: common.BytesToHash([]byte{0xd}), Number: big.NewInt(9), Difficulty: big.NewInt(100)},
			},
			ethash.staleResultCh,
			0,
			true,
		},
		// Case4: submit very old solution
		{
			[]*types.Header{
				{ParentHash: common.BytesToHash([]byte{0xe}), Number: big.NewInt(10), Difficulty: big.NewInt(100)},
				{ParentHash: common.BytesToHash([]byte{0xf}), Number: big.NewInt(17), Difficulty: big.NewInt(100)},
			},
			nil,
			0,
			false,
		},
	}

	for id, c := range testcases {
		for _, h := range c.headers {
			ethash.Seal(nil, types.NewBlockWithHeader(h), nil)
		}
		stop := make(chan struct{})
		go func(stop chan struct{}) {
			select {
			case res := <-c.resCh:
				if res.Header().Nonce != fakeNonce {
					t.Errorf("case %d block nonce mismatch, want %s, get %s", id+1, fakeNonce, res.Header().Nonce)
				}
				if res.Header().MixDigest != fakeDigest {
					t.Errorf("case %d block digest mismatch, want %s, get %s", id+1, fakeDigest, res.Header().MixDigest)
				}
				if res.Header().Difficulty.Uint64() != c.headers[c.submitIndex].Difficulty.Uint64() {
					t.Errorf("case %d block difficulty mismatch, want %d, get %d", id+1, c.headers[c.submitIndex].Difficulty, res.Header().Difficulty)
				}
			case <-time.NewTimer(time.Second).C:
				t.Errorf("case %d fetch ethash result timeout", id+1)
			case <-stop:
				return
			}
		}(stop)
		if res := api.SubmitWork(fakeNonce, ethash.SealHash(c.headers[c.submitIndex]), fakeDigest); res != c.submitRes {
			t.Errorf("case %d submit result mismatch, want %t, get %t", id+1, c.submitRes, res)
		}
		close(stop)
	}
}
