// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package state

import (
	"bytes"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie"
)

// testAccount is the data associated with an account used by the state tests.
type testAccount struct {
	address common.Address
	balance *big.Int
	nonce   uint64
	code    []byte
}

// makeTestState create a sample test state to test node-wise reconstruction.
func makeTestState(referrers []common.Hash) (ethdb.Database, common.Hash, []*testAccount) {
	// Create an empty state
	db, _ := ethdb.NewMemDatabase()
	state, _ := New(common.Hash{}, db)

	// Fill it with some arbitrary data
	accounts := []*testAccount{}
	for i := byte(0); i < 96; i++ {
		obj := state.GetOrNewStateObject(common.BytesToAddress([]byte{i}))
		acc := &testAccount{address: common.BytesToAddress([]byte{i})}

		obj.AddBalance(big.NewInt(int64(11 * i)))
		acc.balance = big.NewInt(int64(11 * i))

		obj.SetNonce(uint64(42 * i))
		acc.nonce = uint64(42 * i)

		if i%3 == 0 {
			obj.SetCode([]byte{i, i, i, i, i})
			acc.code = []byte{i, i, i, i, i}
		}
		state.UpdateStateObject(obj)
		accounts = append(accounts, acc)
	}
	root, _ := state.CommitIndexed(referrers)

	// Remove any potentially cached data from the test state creation
	trie.ClearGlobalCache()

	// Return the generated state
	return db, root, accounts
}

// checkStateAccounts cross references a reconstructed state with an expected
// account array.
func checkStateAccounts(t *testing.T, db ethdb.Database, root common.Hash, accounts []*testAccount, parent common.Hash) {
	// Remove any potentially cached data from the state synchronisation
	trie.ClearGlobalCache()

	// Check root availability and state contents
	state, err := New(root, db)
	if err != nil {
		t.Fatalf("failed to create state trie at %x: %v", root, err)
	}
	if err := checkStateConsistency(db, root); err != nil {
		t.Fatalf("inconsistent state trie at %x: %v", root, err)
	}
	if err := checkStateIndex(db, root, parent); err != nil {
		t.Fatalf("index error at %x: %v", root, err)
	}
	for i, acc := range accounts {
		if balance := state.GetBalance(acc.address); balance.Cmp(acc.balance) != 0 {
			t.Errorf("account %d: balance mismatch: have %v, want %v", i, balance, acc.balance)
		}
		if nonce := state.GetNonce(acc.address); nonce != acc.nonce {
			t.Errorf("account %d: nonce mismatch: have %v, want %v", i, nonce, acc.nonce)
		}
		if code := state.GetCode(acc.address); bytes.Compare(code, acc.code) != 0 {
			t.Errorf("account %d: code mismatch: have %x, want %x", i, code, acc.code)
		}
	}
}

// checkStateConsistency checks that all nodes in a state trie and indeed present.
func checkStateConsistency(db ethdb.Database, root common.Hash) (failure error) {
	// Capture any panics by the iterator
	defer func() {
		if r := recover(); r != nil {
			failure = fmt.Errorf("%v", r)
		}
	}()
	// Remove any potentially cached data from the test state creation or previous checks
	trie.ClearGlobalCache()

	// Create and iterate a state trie rooted in a sub-node
	if _, err := db.Get(root.Bytes()); err != nil {
		return
	}
	state, err := New(root, db)
	if err != nil {
		return
	}
	for it := NewNodeIterator(state); it.Next(); {
	}
	return nil
}

// checkStateIndex iterates over the entire state trie and checks that all required
// database indexes have been generated by the synchronizer.
func checkStateIndex(db ethdb.Database, root common.Hash, parent common.Hash) error {
	// Remove any potentially cached data from the test state creation or previous checks
	trie.ClearGlobalCache()

	if _, err := db.Get(root.Bytes()); err != nil {
		return err
	}
	state, err := New(root, db)
	if err != nil {
		return fmt.Errorf("failed to create state trie at %x: %v", root, err)
	}
	// Gather all the indexes that should be present in the database
	indexes := make(map[string]struct{})
	for it := NewNodeIterator(state); it.Next(); {
		if (it.Hash != common.Hash{}) && (it.Parent != common.Hash{}) {
			indexes[string(trie.ParentReferenceIndexKey(it.Parent.Bytes(), it.Hash.Bytes()))] = struct{}{}
		}
	}
	if parent != (common.Hash{}) {
		indexes[string(trie.ParentReferenceIndexKey(parent.Bytes(), root.Bytes()))] = struct{}{}
	}
	// Cross check the indexes and the database itself
	for index, _ := range indexes {
		if _, err := db.Get([]byte(index)); err != nil {
			return fmt.Errorf("failed to retrieve reported index %x: %v", index, err)
		}
	}
	for _, key := range db.(*ethdb.MemDatabase).Keys() {
		if bytes.HasPrefix(key, trie.ParentReferenceIndexPrefix) {
			if _, ok := indexes[string(key)]; !ok {
				return fmt.Errorf("index entry not reported %x", key)
			}
		}
	}
	return nil
}

// Tests that an empty state is not scheduled for syncing.
func TestEmptyStateSyncDangling(t *testing.T) { testEmptyStateSync(t, common.Hash{}) }
func TestEmptyStateSyncRooted(t *testing.T)   { testEmptyStateSync(t, common.BytesToHash([]byte{0x01})) }

func testEmptyStateSync(t *testing.T, origin common.Hash) {
	empty := common.HexToHash("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	db, _ := ethdb.NewMemDatabase()
	if req := NewStateSync(empty, db, origin).Missing(1); len(req) != 0 {
		t.Errorf("content requested for empty state: %v", req)
	}
}

// Tests that given a root hash, a state can sync iteratively on a single thread,
// requesting retrieval tasks and returning all of them in one go.
func TestIterativeStateSyncIndividualDangling(t *testing.T) {
	testIterativeStateSync(t, 1, common.Hash{})
}
func TestIterativeStateSyncIndividualRooted(t *testing.T) {
	testIterativeStateSync(t, 1, common.BytesToHash([]byte{0x02}))
}
func TestIterativeStateSyncBatchedDangling(t *testing.T) {
	testIterativeStateSync(t, 100, common.Hash{})
}
func TestIterativeStateSyncBatchedRooted(t *testing.T) {
	testIterativeStateSync(t, 100, common.BytesToHash([]byte{0x03}))
}

func testIterativeStateSync(t *testing.T, batch int, origin common.Hash) {
	// Create a random state to copy
	srcDb, srcRoot, srcAccounts := makeTestState(nil)

	// Create a destination state and sync with the scheduler
	dstDb, _ := ethdb.NewMemDatabase()
	sched := NewStateSync(srcRoot, dstDb, origin)

	queue := append([]common.Hash{}, sched.Missing(batch)...)
	for len(queue) > 0 {
		results := make([]trie.SyncResult, len(queue))
		for i, hash := range queue {
			data, err := srcDb.Get(hash.Bytes())
			if err != nil {
				t.Fatalf("failed to retrieve node data for %x: %v", hash, err)
			}
			results[i] = trie.SyncResult{hash, data}
		}
		if index, err := sched.Process(results); err != nil {
			t.Fatalf("failed to process result #%d: %v", index, err)
		}
		queue = append(queue[:0], sched.Missing(batch)...)
	}
	// Cross check that the two states are in sync
	checkStateAccounts(t, dstDb, srcRoot, srcAccounts, origin)
}

// Tests that the trie scheduler can correctly reconstruct the state even if only
// partial results are returned, and the others sent only later.
func TestIterativeDelayedStateSyncDangling(t *testing.T) {
	testIterativeDelayedStateSync(t, common.Hash{})
}
func TestIterativeDelayedStateSyncRooted(t *testing.T) {
	testIterativeDelayedStateSync(t, common.BytesToHash([]byte{0x04}))
}

func testIterativeDelayedStateSync(t *testing.T, origin common.Hash) {
	// Create a random state to copy
	srcDb, srcRoot, srcAccounts := makeTestState(nil)

	// Create a destination state and sync with the scheduler
	dstDb, _ := ethdb.NewMemDatabase()
	sched := NewStateSync(srcRoot, dstDb, origin)

	queue := append([]common.Hash{}, sched.Missing(0)...)
	for len(queue) > 0 {
		// Sync only half of the scheduled nodes
		results := make([]trie.SyncResult, len(queue)/2+1)
		for i, hash := range queue[:len(results)] {
			data, err := srcDb.Get(hash.Bytes())
			if err != nil {
				t.Fatalf("failed to retrieve node data for %x: %v", hash, err)
			}
			results[i] = trie.SyncResult{hash, data}
		}
		if index, err := sched.Process(results); err != nil {
			t.Fatalf("failed to process result #%d: %v", index, err)
		}
		queue = append(queue[len(results):], sched.Missing(0)...)
	}
	// Cross check that the two states are in sync
	checkStateAccounts(t, dstDb, srcRoot, srcAccounts, origin)
}

// Tests that given a root hash, a trie can sync iteratively on a single thread,
// requesting retrieval tasks and returning all of them in one go, however in a
// random order.
func TestIterativeRandomStateSyncIndividualDangling(t *testing.T) {
	testIterativeRandomStateSync(t, 1, common.Hash{})
}
func TestIterativeRandomStateSyncIndividualRooted(t *testing.T) {
	testIterativeRandomStateSync(t, 1, common.BytesToHash([]byte{0x05}))
}
func TestIterativeRandomStateSyncBatchedDangling(t *testing.T) {
	testIterativeRandomStateSync(t, 100, common.Hash{})
}
func TestIterativeRandomStateSyncBatchedRooted(t *testing.T) {
	testIterativeRandomStateSync(t, 100, common.BytesToHash([]byte{0x06}))
}

func testIterativeRandomStateSync(t *testing.T, batch int, origin common.Hash) {
	// Create a random state to copy
	srcDb, srcRoot, srcAccounts := makeTestState(nil)

	// Create a destination state and sync with the scheduler
	dstDb, _ := ethdb.NewMemDatabase()
	sched := NewStateSync(srcRoot, dstDb, origin)

	queue := make(map[common.Hash]struct{})
	for _, hash := range sched.Missing(batch) {
		queue[hash] = struct{}{}
	}
	for len(queue) > 0 {
		// Fetch all the queued nodes in a random order
		results := make([]trie.SyncResult, 0, len(queue))
		for hash, _ := range queue {
			data, err := srcDb.Get(hash.Bytes())
			if err != nil {
				t.Fatalf("failed to retrieve node data for %x: %v", hash, err)
			}
			results = append(results, trie.SyncResult{hash, data})
		}
		// Feed the retrieved results back and queue new tasks
		if index, err := sched.Process(results); err != nil {
			t.Fatalf("failed to process result #%d: %v", index, err)
		}
		queue = make(map[common.Hash]struct{})
		for _, hash := range sched.Missing(batch) {
			queue[hash] = struct{}{}
		}
	}
	// Cross check that the two states are in sync
	checkStateAccounts(t, dstDb, srcRoot, srcAccounts, origin)
}

// Tests that the trie scheduler can correctly reconstruct the state even if only
// partial results are returned (Even those randomly), others sent only later.
func TestIterativeRandomDelayedStateSyncDangling(t *testing.T) {
	testIterativeRandomDelayedStateSync(t, common.Hash{})
}
func TestIterativeRandomDelayedStateSyncRooted(t *testing.T) {
	testIterativeRandomDelayedStateSync(t, common.BytesToHash([]byte{0x07}))
}

func testIterativeRandomDelayedStateSync(t *testing.T, origin common.Hash) {
	// Create a random state to copy
	srcDb, srcRoot, srcAccounts := makeTestState(nil)

	// Create a destination state and sync with the scheduler
	dstDb, _ := ethdb.NewMemDatabase()
	sched := NewStateSync(srcRoot, dstDb, origin)

	queue := make(map[common.Hash]struct{})
	for _, hash := range sched.Missing(0) {
		queue[hash] = struct{}{}
	}
	for len(queue) > 0 {
		// Sync only half of the scheduled nodes, even those in random order
		results := make([]trie.SyncResult, 0, len(queue)/2+1)
		for hash, _ := range queue {
			delete(queue, hash)

			data, err := srcDb.Get(hash.Bytes())
			if err != nil {
				t.Fatalf("failed to retrieve node data for %x: %v", hash, err)
			}
			results = append(results, trie.SyncResult{hash, data})

			if len(results) >= cap(results) {
				break
			}
		}
		// Feed the retrieved results back and queue new tasks
		if index, err := sched.Process(results); err != nil {
			t.Fatalf("failed to process result #%d: %v", index, err)
		}
		for _, hash := range sched.Missing(0) {
			queue[hash] = struct{}{}
		}
	}
	// Cross check that the two states are in sync
	checkStateAccounts(t, dstDb, srcRoot, srcAccounts, origin)
}

// Tests that at any point in time during a sync, only complete sub-tries are in
// the database.
func TestIncompleteStateSync(t *testing.T) {
	// Create a random state to copy
	srcDb, srcRoot, srcAccounts := makeTestState(nil)

	// Create a destination state and sync with the scheduler
	dstDb, _ := ethdb.NewMemDatabase()
	sched := NewStateSync(srcRoot, dstDb, common.Hash{})

	added := []common.Hash{}
	queue := append([]common.Hash{}, sched.Missing(1)...)
	for len(queue) > 0 {
		// Fetch a batch of state nodes
		results := make([]trie.SyncResult, len(queue))
		for i, hash := range queue {
			data, err := srcDb.Get(hash.Bytes())
			if err != nil {
				t.Fatalf("failed to retrieve node data for %x: %v", hash, err)
			}
			results[i] = trie.SyncResult{hash, data}
		}
		// Process each of the state nodes
		if index, err := sched.Process(results); err != nil {
			t.Fatalf("failed to process result #%d: %v", index, err)
		}
		for _, result := range results {
			added = append(added, result.Hash)
		}
		// Check that all known sub-tries in the synced state is complete
		for _, root := range added {
			// Skim through the accounts and make sure the root hash is not a code node
			codeHash := false
			for _, acc := range srcAccounts {
				if bytes.Compare(root.Bytes(), crypto.Sha3(acc.code)) == 0 {
					codeHash = true
					break
				}
			}
			// If the root is a real trie node, check consistency
			if !codeHash {
				if err := checkStateConsistency(dstDb, root); err != nil {
					t.Fatalf("state inconsistent: %v", err)
				}
			}
		}
		// Fetch the next batch to retrieve
		queue = append(queue[:0], sched.Missing(1)...)
	}
	// Sanity check that removing any node from the database is detected
	for _, node := range added[1:] {
		key := node.Bytes()
		value, _ := dstDb.Get(key)

		dstDb.Delete(key)
		if err := checkStateConsistency(dstDb, added[0]); err == nil {
			t.Fatalf("trie inconsistency not caught, missing: %x", key)
		}
		dstDb.Put(key, value)
	}
}
