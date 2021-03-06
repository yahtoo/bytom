package txdb

import (
	"fmt"

	"github.com/golang/protobuf/proto"
	dbm "github.com/tendermint/tmlibs/db"

	"github.com/bytom/blockchain/txdb/internal/storage"
	"github.com/bytom/errors"
	"github.com/bytom/protocol/bc"
	"github.com/bytom/protocol/patricia"
	"github.com/bytom/protocol/state"
)

func calcSnapshotKey(hash *bc.Hash) []byte {
	return []byte(fmt.Sprintf("S:%v", hash.String()))
}

// DecodeSnapshot decodes a snapshot from bytes
func DecodeSnapshot(data []byte) (*state.Snapshot, error) {
	var storedSnapshot storage.Snapshot
	if err := proto.Unmarshal(data, &storedSnapshot); err != nil {
		return nil, errors.Wrap(err, "unmarshaling state snapshot proto")
	}

	tree := new(patricia.Tree)
	for _, node := range storedSnapshot.Nodes {
		if err := tree.Insert(node.Key); err != nil {
			return nil, errors.Wrap(err, "reconstructing state tree")
		}
	}

	nonces := make(map[bc.Hash]uint64, len(storedSnapshot.Nonces))
	for _, nonce := range storedSnapshot.Nonces {
		var b32 [32]byte
		copy(b32[:], nonce.Hash)
		hash := bc.NewHash(b32)
		nonces[hash] = nonce.ExpiryMs
	}

	return &state.Snapshot{
		Tree:   tree,
		Nonces: nonces,
	}, nil
}

func saveSnapshot(db dbm.DB, snapshot *state.Snapshot, hash *bc.Hash) error {
	var storedSnapshot storage.Snapshot
	err := patricia.Walk(snapshot.Tree, func(key []byte) error {
		n := &storage.Snapshot_StateTreeNode{Key: key}
		storedSnapshot.Nodes = append(storedSnapshot.Nodes, n)
		return nil
	})
	if err != nil {
		return errors.Wrap(err, "walking patricia tree")
	}

	storedSnapshot.Nonces = make([]*storage.Snapshot_Nonce, 0, len(snapshot.Nonces))
	for k, v := range snapshot.Nonces {
		storedSnapshot.Nonces = append(storedSnapshot.Nonces, &storage.Snapshot_Nonce{
			Hash:     k.Bytes(),
			ExpiryMs: v,
		})
	}

	b, err := proto.Marshal(&storedSnapshot)
	if err != nil {
		return errors.Wrap(err, "marshaling state snapshot")
	}

	db.Set(calcSnapshotKey(hash), b)
	db.SetSync(nil, nil)
	return nil
}

func getSnapshot(db dbm.DB, hash *bc.Hash) (*state.Snapshot, error) {
	data := db.Get(calcSnapshotKey(hash))
	if data == nil {
		return nil, errors.New("no this snapshot.")
	}

	snapshot, err := DecodeSnapshot(data)
	if err != nil {
		return nil, errors.Wrap(err, "decoding snapshot")
	}
	return snapshot, nil
}
